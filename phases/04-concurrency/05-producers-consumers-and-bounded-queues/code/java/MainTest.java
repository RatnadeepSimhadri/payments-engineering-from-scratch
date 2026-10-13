// Tests for phases/04-concurrency/05-producers-consumers-and-bounded-queues. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.concurrent.CountDownLatch;

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

    /** Spin until the predicate holds; fail loudly instead of hanging if it never does. */
    static void spinUntil(String what, java.util.function.BooleanSupplier predicate) {
        long deadline = System.nanoTime() + 5_000_000_000L;
        while (!predicate.getAsBoolean()) {
            if (System.nanoTime() > deadline) throw new IllegalStateException("timed out waiting for " + what);
            Thread.onSpinWait();
        }
    }

    static Payment pay(String id) { return new Payment(id, Money.ofMinor(100, Currency.USD)); }

    public static void main(String[] args) throws Exception {
        // FIFO and the ring buffer
        BoundedBlockingQueue<String> q = new BoundedBlockingQueue<>(3);
        q.put("a"); q.put("b"); q.put("c");
        check("size after three puts", q.size() == 3 && q.isFull());
        check("take returns in FIFO order", q.take().equals("a") && q.take().equals("b"));
        q.put("d"); q.put("e");                                 // tail wraps around the array
        check("ring buffer wraps correctly", q.take().equals("c") && q.take().equals("d") && q.take().equals("e") && q.isEmpty());
        check("high-water mark tracks the maximum size", q.stats().highWaterMark() == 3);

        // Non-blocking offer and poll (timeout 0)
        BoundedBlockingQueue<String> full = new BoundedBlockingQueue<>(2);
        check("offer accepts while there is room", full.offer("x", 0) && full.offer("y", 0));
        check("offer refuses when full and leaves size unchanged", !full.offer("z", 0) && full.size() == 2);
        check("poll returns items then empty", full.poll(0).equals(Optional.of("x")) && full.poll(0).equals(Optional.of("y")) && full.poll(0).isEmpty());

        // Timed offer and poll with a fake clock: no real waiting, exact elapsed time
        FakeClock clock = new FakeClock(1_000);
        BoundedBlockingQueue<String> timed = new BoundedBlockingQueue<>(1, clock);
        timed.put("only");
        check("timed offer on a full queue returns false", !timed.offer("more", 250_000_000L));
        check("the fake clock advanced by exactly the timeout", clock.nanos() == 1_000 + 250_000_000L);
        check("one producer wait was recorded", timed.stats().producerWaits() == 1 && timed.stats().waitingProducers() == 0);
        long before = clock.nanos();
        check("timed poll on a non-empty queue returns at once", timed.poll(250_000_000L).equals(Optional.of("only")) && clock.nanos() == before);
        check("timed poll on an empty queue returns empty after the timeout", timed.poll(40_000_000L).isEmpty() && clock.nanos() == before + 40_000_000L);
        check("one consumer wait was recorded", timed.stats().consumerWaits() == 1);
        check("timed offer on a non-full queue returns true without waiting", timed.offer("back", 250_000_000L) && clock.nanos() == before + 40_000_000L);

        // Validation
        checkThrows("capacity 0 is rejected", IllegalArgumentException.class, () -> new BoundedBlockingQueue<String>(0));
        checkThrows("negative capacity is rejected", IllegalArgumentException.class, () -> new BoundedBlockingQueue<String>(-1));
        checkThrows("null item is rejected", NullPointerException.class, () -> {
            try { new BoundedBlockingQueue<String>(1).put(null); } catch (InterruptedException e) { throw new RuntimeException(e); }
        });

        // A blocked producer wakes when a slot opens
        BoundedBlockingQueue<String> one = new BoundedBlockingQueue<>(1);
        one.put("first");
        CountDownLatch producerDone = new CountDownLatch(1);
        Thread producer = new Thread(() -> {
            try { one.put("second"); } catch (InterruptedException e) { Thread.currentThread().interrupt(); }
            producerDone.countDown();
        });
        producer.start();
        spinUntil("producer to block", () -> one.stats().waitingProducers() == 1);
        check("producer is blocked while the queue is full", one.size() == 1 && one.stats().waitingProducers() == 1);
        check("take frees a slot", one.take().equals("first"));
        producerDone.await();
        check("the blocked producer completed its put", one.take().equals("second") && one.stats().waitingProducers() == 0);

        // A blocked consumer wakes when an item arrives
        BoundedBlockingQueue<String> empty = new BoundedBlockingQueue<>(1);
        String[] received = new String[1];
        CountDownLatch consumerDone = new CountDownLatch(1);
        Thread consumer = new Thread(() -> {
            try { received[0] = empty.take(); } catch (InterruptedException e) { Thread.currentThread().interrupt(); }
            consumerDone.countDown();
        });
        consumer.start();
        spinUntil("consumer to block", () -> empty.stats().waitingConsumers() == 1);
        empty.put("hello");
        consumerDone.await();
        check("the blocked consumer received the item", "hello".equals(received[0]) && empty.stats().consumerWaits() >= 1);

        // Backpressure: the consumer does not start until the producer is stuck at the bound
        BoundedBlockingQueue<Payment> pipe = new BoundedBlockingQueue<>(4);
        List<Payment> script = Producer.script("auth", 20, 1_999);
        CountDownLatch done = new CountDownLatch(1);
        new Thread(new Producer(pipe, script, done)).start();
        spinUntil("producer to hit the bound", () -> pipe.stats().waitingProducers() == 1);
        check("queue is exactly at capacity while the producer waits", pipe.size() == 4);
        Money posted = Money.ofMinor(0, Currency.USD);
        boolean fifo = true;
        for (int i = 0; i < script.size(); i++) {
            Payment p = pipe.take();
            fifo &= p.id().equals(script.get(i).id());
            posted = posted.plus(p.amount());
        }
        done.await();
        check("every payment arrived in FIFO order", fifo);
        check("money is conserved across the queue", posted.equals(Money.ofMinor(20 * 1_999L, Currency.USD)));
        check("high-water mark never exceeded capacity", pipe.stats().highWaterMark() == 4);
        check("the producer waited at least once", pipe.stats().producerWaits() >= 1 && pipe.stats().waitingProducers() == 0);

        // Many producers, many consumers: nothing lost, nothing duplicated, per-producer order kept
        BoundedBlockingQueue<Payment> shared = new BoundedBlockingQueue<>(8);
        int producers = 4, perProducer = 500, consumers = 2;
        CountDownLatch producersDone = new CountDownLatch(producers);
        for (int p = 0; p < producers; p++) {
            new Thread(new Producer(shared, Producer.script("p" + p, perProducer, 1), producersDone)).start();
        }
        List<List<Payment>> seen = new ArrayList<>();
        CountDownLatch consumersDone = new CountDownLatch(consumers);
        int total = producers * perProducer;
        for (int c = 0; c < consumers; c++) {
            List<Payment> mine = new ArrayList<>();
            seen.add(mine);
            new Thread(() -> {
                try {
                    for (int i = 0; i < total / consumers; i++) mine.add(shared.take());
                } catch (InterruptedException e) {
                    Thread.currentThread().interrupt();
                } finally {
                    consumersDone.countDown();
                }
            }).start();
        }
        producersDone.await();
        consumersDone.await();
        Set<String> ids = new HashSet<>();
        Map<String, Integer> lastSeq = new HashMap<>();
        boolean perProducerOrder = true;
        for (List<Payment> list : seen) {
            for (Payment p : list) {
                ids.add(p.id());
                String producer0 = p.id().substring(0, p.id().indexOf('-'));
                int seq = Integer.parseInt(p.id().substring(p.id().indexOf('-') + 1));
                perProducerOrder &= seq > lastSeq.getOrDefault(producer0 + "@" + list.hashCode(), -1);
                lastSeq.put(producer0 + "@" + list.hashCode(), seq);
            }
        }
        check("every item was consumed exactly once", ids.size() == total && shared.isEmpty());
        check("each consumer saw each producer's items in increasing order", perProducerOrder);
        check("high-water mark bounded by capacity under 4x2 load", shared.stats().highWaterMark() <= 8);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
