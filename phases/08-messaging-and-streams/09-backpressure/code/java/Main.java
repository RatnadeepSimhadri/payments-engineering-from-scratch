// Lesson: phases/08-messaging-and-streams/09-backpressure/docs/en.md
// Backpressure and Flow Control
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Java 21, standard library only. Pull-based demand follows the Reactive Streams specification
// (Subscription.request(n), rule 3.17: demand is additive and never exceeds Long.MAX_VALUE).

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.Objects;
import java.util.Random;

/** ISO 4217 currency: three-letter code plus minor-unit exponent. Copied from Phase 10, Lesson 01. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);

    Currency {
        if (code == null || code.length() != 3) throw new IllegalArgumentException("currency code must be three letters");
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4");
    }
}

/** Integer minor units plus a currency. Never a float. */
record Money(long minorUnits, Currency currency) {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    static Money zero(Currency currency) { return new Money(0, currency); }

    Money plus(Money other) {
        if (!currency.equals(other.currency)) {
            throw new IllegalArgumentException("cannot combine " + currency.code() + " with " + other.currency.code());
        }
        return new Money(Math.addExact(minorUnits, other.minorUnits), currency);
    }

    @Override public String toString() {
        long scale = 1;
        for (int i = 0; i < currency.exponent(); i++) scale *= 10;
        long abs = Math.abs(minorUnits);
        String frac = currency.exponent() == 0 ? "" : "." + String.format("%0" + currency.exponent() + "d", abs % scale);
        return (minorUnits < 0 ? "-" : "") + (abs / scale) + frac + " " + currency.code();
    }
}

/** A payment event flowing from the authorization service to the ledger poster. */
record PaymentEvent(long id, long producedAtTick, Money amount) {}

/** What a bounded queue does when it is full and another item is offered. */
enum OverflowPolicy {
    /** Refuse the item; the caller keeps it and must retry. The producer slows down. */
    BLOCK,
    /** Refuse and discard the new item. Freshest data lost. */
    DROP_NEWEST,
    /** Evict the head to make room. Oldest data lost. */
    DROP_OLDEST
}

/** Outcome of a single offer. */
enum OfferResult { ACCEPTED, REJECTED_FULL, DROPPED_NEWEST, EVICTED_OLDEST }

/**
 * A FIFO queue with a hard capacity and an explicit overflow policy. Capacity Integer.MAX_VALUE models
 * the "unbounded" queue that every system has by default and that is the thing this lesson argues against.
 */
final class BoundedQueue<T> {
    private final ArrayDeque<T> items = new ArrayDeque<>();
    private final int capacity;
    private final OverflowPolicy policy;
    private long dropped = 0;
    private int maxDepth = 0;

    BoundedQueue(int capacity, OverflowPolicy policy) {
        if (capacity <= 0) throw new IllegalArgumentException("capacity must be positive");
        this.capacity = capacity;
        this.policy = Objects.requireNonNull(policy);
    }

    OfferResult offer(T item) {
        Objects.requireNonNull(item);
        if (items.size() < capacity) {
            items.addLast(item);
            maxDepth = Math.max(maxDepth, items.size());
            return OfferResult.ACCEPTED;
        }
        return switch (policy) {
            case BLOCK -> OfferResult.REJECTED_FULL;
            case DROP_NEWEST -> { dropped++; yield OfferResult.DROPPED_NEWEST; }
            case DROP_OLDEST -> { items.pollFirst(); dropped++; items.addLast(item); yield OfferResult.EVICTED_OLDEST; }
        };
    }

    T poll() { return items.pollFirst(); }

    int size() { return items.size(); }

    boolean isFull() { return items.size() >= capacity; }

    int capacity() { return capacity; }

    long dropped() { return dropped; }

    int maxDepth() { return maxDepth; }
}

/**
 * Pull-based demand, as in Reactive Streams. The consumer calls request(n) to say how many more items
 * it can take; the producer may emit only while outstanding demand is positive. Demand is additive and
 * saturates at Long.MAX_VALUE instead of overflowing (spec rule 3.17).
 */
final class DemandSubscription {
    private long demand = 0;

    void request(long n) {
        if (n <= 0) throw new IllegalArgumentException("request(n) requires n > 0 (Reactive Streams rule 3.9)");
        long next = demand + n;
        demand = next < 0 ? Long.MAX_VALUE : next;   // saturate, never wrap
    }

    boolean hasDemand() { return demand > 0; }

    /** Consume one unit of demand; returns false if none was available. */
    boolean tryConsumeOne() {
        if (demand <= 0) return false;
        if (demand != Long.MAX_VALUE) demand--;
        return true;
    }

    long outstanding() { return demand; }
}

/** The five ways to connect a fast producer to a slow consumer. */
enum Strategy { UNBOUNDED, DROP_NEWEST, DROP_OLDEST, BLOCK, PULL }

/**
 * What happened over a run. Every count is exact and the two accounting identities tie them together:
 *   offeredLoad == produced + throttled           (every request was either admitted or turned away)
 *   produced == delivered + dropped + inFlight + inQueue   (every admitted event is in exactly one place)
 */
record Stats(Strategy strategy, long offeredLoad, long produced, long throttled, long delivered, long dropped,
             long inFlight, long inQueue, long rejectedOffers, long producerBlockedTicks, int maxQueueDepth,
             long maxLatencyTicks, long p99LatencyTicks, Money deliveredAmount) {

    boolean conserves() {
        return offeredLoad == produced + throttled && produced == delivered + dropped + inFlight + inQueue;
    }
}

/**
 * A deterministic tick-based simulation of one producer, one queue, one consumer. Upstream requests
 * arrive at ~producerRate per tick with seeded jitter; the consumer drains consumerRate per tick but
 * stalls (processes nothing) on a seeded fraction of ticks, like a GC pause or a slow downstream call.
 * The producer is a request handler: while it is blocked holding an event the queue refused, it cannot
 * admit new requests, and those are counted as throttled (the HTTP 429 / slow-accept the caller sees).
 * No threads, no sleeping: the tick counter is the clock.
 */
final class FlowSimulation {
    private final Strategy strategy;
    private final int producerRate;
    private final int producerJitter;
    private final int consumerRate;
    private final double consumerStallProbability;
    private final int queueCapacity;
    private final Random random;

    FlowSimulation(Strategy strategy, int producerRate, int producerJitter, int consumerRate,
                   double consumerStallProbability, int queueCapacity, long seed) {
        this.strategy = strategy;
        this.producerRate = producerRate;
        this.producerJitter = producerJitter;
        this.consumerRate = consumerRate;
        this.consumerStallProbability = consumerStallProbability;
        this.queueCapacity = queueCapacity;
        this.random = new Random(seed);
    }

    Stats run(int ticks) {
        OverflowPolicy policy = switch (strategy) {
            case DROP_NEWEST -> OverflowPolicy.DROP_NEWEST;
            case DROP_OLDEST -> OverflowPolicy.DROP_OLDEST;
            default -> OverflowPolicy.BLOCK;
        };
        int capacity = strategy == Strategy.UNBOUNDED ? Integer.MAX_VALUE : queueCapacity;
        BoundedQueue<PaymentEvent> queue = new BoundedQueue<>(capacity, policy);
        DemandSubscription subscription = new DemandSubscription();
        if (strategy == Strategy.PULL) subscription.request(queueCapacity);   // initial demand = buffer size

        PaymentEvent inFlight = null;                 // produced, refused by the queue, held by the blocked producer
        List<Long> latencies = new ArrayList<>();
        long offered = 0, produced = 0, throttled = 0, delivered = 0, rejected = 0, blockedTicks = 0, nextId = 1;
        Money deliveredAmount = Money.zero(Currency.USD);

        for (int tick = 0; tick < ticks; tick++) {
            boolean blockedThisTick = false;

            // Producer, step 1: retry the event it is holding from last tick, if any.
            if (inFlight != null) {
                if (queue.offer(inFlight) == OfferResult.REJECTED_FULL) { rejected++; blockedThisTick = true; }
                else inFlight = null;
            }

            // Producer, step 2: admit this tick's requests, one at a time, until blocked.
            int want = producerRate + (producerJitter == 0 ? 0 : random.nextInt(producerJitter + 1));
            for (int i = 0; i < want; i++) {
                offered++;
                long cents = 100 + random.nextInt(9900);   // drawn for every request so all strategies see the identical load
                if (inFlight != null) { throttled++; continue; }                        // BLOCK: handler is stuck, caller sees 429 / slow accept
                if (strategy == Strategy.PULL && !subscription.tryConsumeOne()) {         // PULL: no demand, refuse before producing
                    throttled++; blockedThisTick = true; continue;
                }
                PaymentEvent e = new PaymentEvent(nextId++, tick, Money.ofMinor(cents, Currency.USD));
                produced++;
                OfferResult r = queue.offer(e);
                if (r == OfferResult.REJECTED_FULL) { rejected++; inFlight = e; blockedThisTick = true; }
                // ACCEPTED, DROPPED_NEWEST, EVICTED_OLDEST: the queue took responsibility for the event
            }
            if (blockedThisTick) blockedTicks++;

            // Consumer: drain up to consumerRate, unless stalled this tick.
            boolean stalled = random.nextDouble() < consumerStallProbability;
            int processed = 0;
            if (!stalled) {
                for (int i = 0; i < consumerRate; i++) {
                    PaymentEvent e = queue.poll();
                    if (e == null) break;
                    delivered++;
                    processed++;
                    latencies.add((long) tick - e.producedAtTick());
                    deliveredAmount = deliveredAmount.plus(e.amount());
                }
            }
            if (strategy == Strategy.PULL && processed > 0) subscription.request(processed);   // demand = capacity just freed
        }

        Collections.sort(latencies);
        long max = latencies.isEmpty() ? 0 : latencies.get(latencies.size() - 1);
        long p99 = latencies.isEmpty() ? 0 : latencies.get((int) Math.min(latencies.size() - 1, Math.floor(latencies.size() * 0.99)));
        return new Stats(strategy, offered, produced, throttled, delivered, queue.dropped(), inFlight == null ? 0 : 1,
                queue.size(), rejected, blockedTicks, queue.maxDepth(), max, p99, deliveredAmount);
    }
}

public final class Main {
    static String row(Stats s) {
        return String.format("%-12s offered=%6d produced=%6d throttled=%5d delivered=%6d dropped=%5d inQueue=%5d rejectedOffers=%5d maxDepth=%6d maxLat=%5d p99Lat=%4d",
                s.strategy(), s.offeredLoad(), s.produced(), s.throttled(), s.delivered(), s.dropped(), s.inQueue(),
                s.rejectedOffers(), s.maxQueueDepth(), s.maxLatencyTicks(), s.p99LatencyTicks());
    }

    public static void main(String[] args) {
        // Authorization service emits ~12 events/tick (10 + jitter 0..4); ledger poster handles 10/tick and stalls 10% of ticks.
        // Sustained overload: the consumer cannot keep up and something has to give.
        final int producerRate = 10, jitter = 4, consumerRate = 10, capacity = 64, ticks = 2000;
        final double stall = 0.10;
        final long seed = 42;

        System.out.println("== Fast producer (~12/tick), slow consumer (10/tick, 10% stalls), queue capacity " + capacity + ", " + ticks + " ticks, seed " + seed + " ==");
        System.out.println("One tick is one scheduling quantum; latency is in ticks from production to delivery.");
        System.out.println();
        for (Strategy s : Strategy.values()) {
            Stats st = new FlowSimulation(s, producerRate, jitter, consumerRate, stall, capacity, seed).run(ticks);
            System.out.println(row(st) + "  conserves=" + st.conserves());
        }

        System.out.println();
        System.out.println("== Same strategies when the consumer is fast enough (12/tick, no stalls): backpressure costs nothing ==");
        for (Strategy s : Strategy.values()) {
            Stats st = new FlowSimulation(s, producerRate, jitter, 13, 0.0, capacity, seed).run(ticks);
            System.out.println(row(st));
        }

        System.out.println();
        System.out.println("== Reading the table ==");
        System.out.println("UNBOUNDED : nothing dropped, nothing throttled, queue depth and latency grow without limit. This is the OOM at 3 a.m.");
        System.out.println("DROP_*    : bounded memory and latency, but admitted payment events are gone. Dropped money is a ledger incident.");
        System.out.println("BLOCK     : bounded memory and latency, nothing admitted is lost; overload surfaces as throttled requests, discovered by collision (rejectedOffers).");
        System.out.println("PULL      : same bounds and the same throttling, but the producer knows the demand up front and never builds an event it cannot deliver.");
    }
}
