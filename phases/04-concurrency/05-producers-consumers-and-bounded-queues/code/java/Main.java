// Lesson: phases/04-concurrency/05-producers-consumers-and-bounded-queues/docs/en.md
// Producers, Consumers, and Bounded Queues
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: java.util.concurrent.locks.Condition Javadoc (spurious wakeups, awaitNanos); Hoare, "Monitors: An
// Operating System Structuring Concept" (1974); ArrayBlockingQueue source (two-condition ring buffer design).
// Java 21, standard library only. Same design as the Python and Go versions.

import java.util.ArrayList;
import java.util.List;
import java.util.Objects;
import java.util.Optional;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.locks.Condition;
import java.util.concurrent.locks.ReentrantLock;

/** ISO 4217 currency: code and minor-unit exponent. Copied from Phase 10, Lesson 01. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);

    Currency {
        if (code == null || code.length() != 3) throw new IllegalArgumentException("currency code must be three letters: " + code);
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4: " + exponent);
    }

    long minorUnitsPerMajor() {
        long scale = 1;
        for (int i = 0; i < exponent; i++) scale *= 10;
        return scale;
    }
}

/** Exact amount in minor units of one currency; immutable, so safe to hand from thread to thread. */
record Money(long minorUnits, Currency currency) {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    Money plus(Money other) {
        if (!currency.equals(other.currency)) throw new IllegalArgumentException("cannot combine " + currency.code() + " with " + other.currency.code());
        return new Money(Math.addExact(minorUnits, other.minorUnits), currency);
    }

    @Override
    public String toString() {
        long magnitude = Math.abs(minorUnits);
        long scale = currency.minorUnitsPerMajor();
        String result = Long.toString(magnitude / scale);
        if (currency.exponent() > 0) {
            String frac = Long.toString(magnitude % scale);
            result = result + "." + "0".repeat(currency.exponent() - frac.length()) + frac;
        }
        return (minorUnits < 0 ? "-" : "") + result + " " + currency.code();
    }
}

/** The unit of work flowing through the queue: an authorization event waiting to be posted to the ledger. */
record Payment(String id, Money amount) {
    Payment {
        Objects.requireNonNull(id, "id");
        Objects.requireNonNull(amount, "amount");
    }
}

/**
 * Time, injected. nanos() reads the clock; await() is a timed wait on a condition. The system clock
 * really waits; the fake clock advances its own time by the requested amount and returns at once,
 * which is exactly what a timed wait that expired with no signal looks like to the caller.
 */
interface Clock {
    long nanos();

    void await(Condition condition, long nanos) throws InterruptedException;

    static Clock system() {
        return new Clock() {
            @Override public long nanos() { return System.nanoTime(); }
            @Override public void await(Condition condition, long nanos) throws InterruptedException { condition.awaitNanos(nanos); }
        };
    }
}

final class FakeClock implements Clock {
    private long now;

    FakeClock(long startNanos) { this.now = startNanos; }

    @Override public long nanos() { return now; }

    /** No real waiting: the clock jumps to the deadline, as if the wait timed out unsignalled. */
    @Override public void await(Condition condition, long nanos) { now += Math.max(0, nanos); }
}

/** Counters the queue keeps under its lock. Wait counts are events; waiting counts are current. */
record QueueStats(long producerWaits, long consumerWaits, int highWaterMark, int waitingProducers, int waitingConsumers) { }

/**
 * A FIFO ring buffer with a fixed capacity, built from one lock and two condition variables.
 * put blocks while full, take blocks while empty; offer and poll give up after a timeout.
 * The bound is the point: a producer that outruns the consumer is slowed down, not buffered forever.
 */
final class BoundedBlockingQueue<T> {
    private final Object[] items;
    private int head;        // next take
    private int tail;        // next put
    private int count;
    private final ReentrantLock lock = new ReentrantLock();
    private final Condition notFull = lock.newCondition();
    private final Condition notEmpty = lock.newCondition();
    private final Clock clock;
    private long producerWaits, consumerWaits;
    private int highWaterMark, waitingProducers, waitingConsumers;

    BoundedBlockingQueue(int capacity) { this(capacity, Clock.system()); }

    BoundedBlockingQueue(int capacity, Clock clock) {
        if (capacity <= 0) throw new IllegalArgumentException("capacity must be positive: " + capacity);
        this.items = new Object[capacity];
        this.clock = Objects.requireNonNull(clock);
    }

    int capacity() { return items.length; }

    int size() {
        lock.lock();
        try { return count; } finally { lock.unlock(); }
    }

    boolean isEmpty() { return size() == 0; }
    boolean isFull() { return size() == items.length; }

    QueueStats stats() {
        lock.lock();
        try {
            return new QueueStats(producerWaits, consumerWaits, highWaterMark, waitingProducers, waitingConsumers);
        } finally {
            lock.unlock();
        }
    }

    /** Blocks while the queue is full. This is backpressure: the caller's thread is the buffer. */
    void put(T item) throws InterruptedException {
        Objects.requireNonNull(item, "item");
        lock.lock();
        try {
            while (count == items.length) {          // while, not if: wakeups can be spurious or stolen
                producerWaits++;
                waitingProducers++;
                try { notFull.await(); } finally { waitingProducers--; }
            }
            enqueue(item);
        } finally {
            lock.unlock();
        }
    }

    /** Blocks while the queue is empty. */
    T take() throws InterruptedException {
        lock.lock();
        try {
            while (count == 0) {
                consumerWaits++;
                waitingConsumers++;
                try { notEmpty.await(); } finally { waitingConsumers--; }
            }
            return dequeue();
        } finally {
            lock.unlock();
        }
    }

    /** Waits at most timeoutNanos for space; false means "shed the load": tell the caller to retry later. */
    boolean offer(T item, long timeoutNanos) throws InterruptedException {
        Objects.requireNonNull(item, "item");
        lock.lock();
        try {
            long deadline = clock.nanos() + timeoutNanos;
            while (count == items.length) {
                long remaining = deadline - clock.nanos();
                if (remaining <= 0) return false;
                producerWaits++;
                waitingProducers++;
                try { clock.await(notFull, remaining); } finally { waitingProducers--; }
            }
            enqueue(item);
            return true;
        } finally {
            lock.unlock();
        }
    }

    /** Waits at most timeoutNanos for an item; empty means the consumer can do housekeeping and come back. */
    Optional<T> poll(long timeoutNanos) throws InterruptedException {
        lock.lock();
        try {
            long deadline = clock.nanos() + timeoutNanos;
            while (count == 0) {
                long remaining = deadline - clock.nanos();
                if (remaining <= 0) return Optional.empty();
                consumerWaits++;
                waitingConsumers++;
                try { clock.await(notEmpty, remaining); } finally { waitingConsumers--; }
            }
            return Optional.of(dequeue());
        } finally {
            lock.unlock();
        }
    }

    // Both helpers assume the lock is held and the precondition (space / an item) was checked.
    private void enqueue(T item) {
        items[tail] = item;
        tail = (tail + 1) % items.length;
        count++;
        if (count > highWaterMark) highWaterMark = count;
        notEmpty.signal();                           // one waiting consumer is enough: one item arrived
    }

    @SuppressWarnings("unchecked")
    private T dequeue() {
        T item = (T) items[head];
        items[head] = null;                          // let the GC have it
        head = (head + 1) % items.length;
        count--;
        notFull.signal();                            // one waiting producer is enough: one slot opened
        return item;
    }
}

/** A producer that puts a scripted list of payments and records how often it had to wait. */
final class Producer implements Runnable {
    private final BoundedBlockingQueue<Payment> queue;
    private final List<Payment> script;
    private final CountDownLatch done;

    Producer(BoundedBlockingQueue<Payment> queue, List<Payment> script, CountDownLatch done) {
        this.queue = queue;
        this.script = script;
        this.done = done;
    }

    @Override
    public void run() {
        try {
            for (Payment p : script) queue.put(p);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        } finally {
            done.countDown();
        }
    }

    static List<Payment> script(String prefix, int n, long minorUnitsEach) {
        List<Payment> list = new ArrayList<>(n);
        for (int i = 0; i < n; i++) list.add(new Payment(prefix + "-" + i, Money.ofMinor(minorUnitsEach, Currency.USD)));
        return list;
    }
}

public final class Main {
    /** Spin (never sleep) until the queue reports a waiting producer; bounded so a bug cannot hang the demo. */
    static void untilProducerWaiting(BoundedBlockingQueue<?> queue) {
        long deadline = System.nanoTime() + 5_000_000_000L;
        while (queue.stats().waitingProducers() == 0) {
            if (System.nanoTime() > deadline) throw new IllegalStateException("producer never blocked");
            Thread.onSpinWait();
        }
    }

    public static void main(String[] args) throws InterruptedException {
        Currency usd = Currency.USD;

        System.out.println("== A full queue refuses, it does not grow ==");
        BoundedBlockingQueue<Payment> ingest = new BoundedBlockingQueue<>(3);
        for (int i = 1; i <= 5; i++) {
            Payment p = new Payment("auth-" + i, Money.ofMinor(1_999, usd));
            boolean accepted = ingest.offer(p, 0);
            System.out.printf("offer %s -> %s  (size %d/%d)%n", p.id(), accepted ? "accepted" : "REFUSED: reply 429 Retry-After", ingest.size(), ingest.capacity());
        }

        System.out.println("\n== A timed offer against a full queue, with a fake clock ==");
        FakeClock clock = new FakeClock(0);
        BoundedBlockingQueue<Payment> timed = new BoundedBlockingQueue<>(1, clock);
        timed.put(new Payment("auth-a", Money.ofMinor(100, usd)));
        boolean accepted = timed.offer(new Payment("auth-b", Money.ofMinor(100, usd)), 250_000_000L);
        System.out.printf("offer with 250 ms timeout -> %s; clock advanced %d ms; producer waits = %d%n",
                accepted, clock.nanos() / 1_000_000, timed.stats().producerWaits());
        Optional<Payment> polled = timed.poll(0);
        System.out.println("poll(0) on the one-item queue -> " + polled.map(Payment::id).orElse("empty")
                + "; poll(0) again -> " + timed.poll(0).map(Payment::id).orElse("empty"));

        System.out.println("\n== Backpressure: a fast producer, a slow consumer, capacity 4 ==");
        BoundedBlockingQueue<Payment> pipe = new BoundedBlockingQueue<>(4);
        List<Payment> script = Producer.script("auth", 20, 1_999);
        CountDownLatch producerDone = new CountDownLatch(1);
        Thread producer = new Thread(new Producer(pipe, script, producerDone), "producer");
        producer.start();
        untilProducerWaiting(pipe);                  // the consumer does not start until the producer is stuck
        System.out.printf("producer blocked with %d/%d items queued; it cannot run ahead of the ledger%n", pipe.size(), pipe.capacity());
        Money posted = Money.ofMinor(0, usd);
        boolean fifo = true;
        for (int i = 0; i < script.size(); i++) {
            Payment p = pipe.take();
            fifo &= p.id().equals(script.get(i).id());
            posted = posted.plus(p.amount());
        }
        producerDone.await();
        QueueStats stats = pipe.stats();
        System.out.printf("consumed %d payments in FIFO order: %s; posted %s%n", script.size(), fifo, posted);
        System.out.printf("high-water mark %d (never above capacity), producer waited %d times, consumer waited %d times%n",
                stats.highWaterMark(), stats.producerWaits(), stats.consumerWaits());

        System.out.println("\n== Without the bound, the backlog lives in your heap ==");
        BoundedBlockingQueue<Payment> wide = new BoundedBlockingQueue<>(1_000);
        for (Payment p : script) wide.put(p);
        System.out.printf("capacity 1000: all 20 queued, producer waited %d times, high-water mark %d: nothing told the producer to slow down%n",
                wide.stats().producerWaits(), wide.stats().highWaterMark());
    }
}
