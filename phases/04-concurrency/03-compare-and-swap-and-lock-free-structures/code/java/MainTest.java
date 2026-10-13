// Tests for phases/04-concurrency/03-compare-and-swap-and-lock-free-structures. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.atomic.AtomicInteger;

public final class MainTest {
    private static int passed = 0;
    private static final List<String> failures = new ArrayList<>();

    static void check(String name, boolean condition) {
        if (condition) passed++; else failures.add(name);
    }

    static void checkThrows(String name, Class<? extends Throwable> type, Runnable action) {
        try {
            action.run();
            failures.add(name + " (no exception)");
        } catch (Throwable t) {
            if (type.isInstance(t)) passed++; else failures.add(name + " (wrong exception " + t.getClass().getSimpleName() + ")");
        }
    }

    public static void main(String[] args) throws Exception {
        treiberBasics();
        treiberStress();
        abaReproduced();
        abaFixedByStamp();
        queueBasics();
        queueStress();
        progressTable();

        System.out.println(passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("  FAIL: " + f);
        System.exit(failures.isEmpty() ? 0 : 1);
    }

    static void treiberBasics() {
        TreiberStack<String> stack = new TreiberStack<>();
        check("new stack is empty", stack.isEmpty() && stack.size() == 0);
        check("pop on empty is Optional.empty", stack.pop().isEmpty());
        check("peek on empty is Optional.empty", stack.peek().isEmpty());
        stack.push("a"); stack.push("b"); stack.push("c");
        check("size after three pushes", stack.size() == 3);
        check("LIFO order", stack.pop().get().equals("c") && stack.pop().get().equals("b") && stack.pop().get().equals("a"));
        check("empty after popping everything", stack.isEmpty() && stack.pop().isEmpty());
        check("no retries when single-threaded", stack.casRetries() == 0);
        checkThrows("push rejects null", NullPointerException.class, () -> stack.push(null));
    }

    static void treiberStress() throws InterruptedException {
        TreiberStack<Long> stack = new TreiberStack<>();
        int threads = 4, perThread = 20_000;
        // Phase 1: everyone pushes, nobody pops -> exactly threads*perThread distinct values must be present.
        CountDownLatch start = new CountDownLatch(1), done = new CountDownLatch(threads);
        for (int t = 0; t < threads; t++) {
            final long base = (long) t * perThread;
            new Thread(() -> {
                try { start.await(); } catch (InterruptedException e) { return; }
                for (int i = 0; i < perThread; i++) stack.push(base + i);
                done.countDown();
            }).start();
        }
        start.countDown();
        done.await();
        check("concurrent pushes: size is exact", stack.size() == threads * perThread);
        Set<Long> seen = new HashSet<>();
        for (Optional<Long> v = stack.pop(); v.isPresent(); v = stack.pop()) seen.add(v.get());
        check("concurrent pushes: every value present exactly once", seen.size() == threads * perThread);

        // Phase 2: mixed push/pop; the stack must end empty and conserve the pop count.
        long elapsed = Main.stressStack(stack, threads, perThread);
        check("mixed push/pop leaves the stack empty", stack.isEmpty());
        check("stress finished in bounded time", elapsed < 10_000);
    }

    /** Reproduces the ABA interleaving deterministically in one thread via the beforeCas seam. */
    static void abaReproduced() {
        PooledStack[] holder = new PooledStack[1];
        boolean[] armed = {true};
        List<String> poppedByB = new ArrayList<>();
        PooledStack stack = new PooledStack(() -> {
            if (!armed[0]) return;
            armed[0] = false;
            PooledStack s = holder[0];
            poppedByB.add(s.pop().get().paymentId());        // pay_003, N3 freed
            poppedByB.add(s.pop().get().paymentId());        // pay_002, N2 freed
            s.push(new CaptureJob("pay_004", 4_000));        // reuses N3: head is N3 again, with different contents
        });
        holder[0] = stack;
        stack.push(new CaptureJob("pay_001", 1_000));
        stack.push(new CaptureJob("pay_002", 2_000));
        stack.push(new CaptureJob("pay_003", 3_000));
        PooledStack.Node headBefore = stack.headNode();
        check("head is N3 before the race", headBefore.id == 3);

        CaptureJob gotByA = stack.pop().get();
        check("B popped pay_003 then pay_002", poppedByB.equals(List.of("pay_003", "pay_002")));
        check("A's stale CAS succeeded because the reference matched", gotByA.paymentId().equals("pay_004"));
        List<String> after = stack.snapshot();
        check("ABA: pay_002 reappears on the stack although it was already popped", after.get(0).equals("N2=pay_002"));
        Optional<CaptureJob> duplicate = stack.pop();
        check("ABA: pay_002 is popped a second time (double capture)", duplicate.isPresent() && duplicate.get().paymentId().equals("pay_002"));
    }

    static void abaFixedByStamp() {
        StampedPooledStack[] holder = new StampedPooledStack[1];
        boolean[] armed = {true};
        StampedPooledStack stack = new StampedPooledStack(() -> {
            if (!armed[0]) return;
            armed[0] = false;
            StampedPooledStack s = holder[0];
            s.pop(); s.pop(); s.push(new CaptureJob("pay_004", 4_000));
        });
        holder[0] = stack;
        stack.push(new CaptureJob("pay_001", 1_000));
        stack.push(new CaptureJob("pay_002", 2_000));
        stack.push(new CaptureJob("pay_003", 3_000));
        check("stamp counts successful CASes", stack.stamp() == 3);

        CaptureJob gotByA = stack.pop().get();
        check("stale CAS was rejected exactly once", stack.staleCasRejected() == 1);
        check("A retried and popped the current top (pay_004)", gotByA.paymentId().equals("pay_004"));
        check("stack holds only pay_001 afterwards", stack.snapshot().equals(List.of("N1=pay_001")));
        check("pay_002 is not popped twice", stack.pop().get().paymentId().equals("pay_001") && stack.pop().isEmpty());
        check("stamp advanced past every operation", stack.stamp() == 8);
    }

    static void queueBasics() {
        MichaelScottQueue<String> q = new MichaelScottQueue<>();
        check("new queue is empty", q.isEmpty() && q.dequeue().isEmpty());
        q.enqueue("a"); q.enqueue("b"); q.enqueue("c");
        check("FIFO order", q.dequeue().get().equals("a") && q.dequeue().get().equals("b") && q.dequeue().get().equals("c"));
        check("empty again", q.isEmpty() && q.dequeue().isEmpty());
        q.enqueue("d");
        check("enqueue after draining works (sentinel moved)", q.dequeue().get().equals("d"));
        checkThrows("enqueue rejects null", NullPointerException.class, () -> q.enqueue(null));
    }

    static void queueStress() throws InterruptedException {
        MichaelScottQueue<Integer> q = new MichaelScottQueue<>();
        int producers = 4, consumers = 4, perProducer = 20_000;
        int total = producers * perProducer;
        ConcurrentHashMap<Integer, Integer> counts = new ConcurrentHashMap<>();
        AtomicInteger consumed = new AtomicInteger();
        CountDownLatch producersDone = new CountDownLatch(producers), consumersDone = new CountDownLatch(consumers);
        for (int p = 0; p < producers; p++) {
            final int base = p * perProducer;
            new Thread(() -> {
                for (int i = 0; i < perProducer; i++) q.enqueue(base + i);
                producersDone.countDown();
            }).start();
        }
        for (int c = 0; c < consumers; c++) {
            new Thread(() -> {
                while (consumed.get() < total) {
                    Optional<Integer> v = q.dequeue();
                    if (v.isPresent()) {
                        counts.merge(v.get(), 1, Integer::sum);
                        consumed.incrementAndGet();
                    } else {
                        Thread.onSpinWait();
                    }
                }
                consumersDone.countDown();
            }).start();
        }
        producersDone.await();
        consumersDone.await();
        check("every enqueued value was dequeued", counts.size() == total);
        check("no value was dequeued twice", counts.values().stream().allMatch(n -> n == 1));
        check("queue is empty after the stress", q.isEmpty());
    }

    static void progressTable() {
        check("four progress levels, weakest first", Progress.values().length == 4 && Progress.values()[0] == Progress.BLOCKING
                && Progress.values()[3] == Progress.WAIT_FREE);
        check("lock-free is strictly weaker than wait-free", Progress.LOCK_FREE.ordinal() < Progress.WAIT_FREE.ordinal());
    }
}
