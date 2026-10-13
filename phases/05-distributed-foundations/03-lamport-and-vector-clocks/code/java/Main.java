// Lesson: phases/05-distributed-foundations/03-lamport-and-vector-clocks/docs/en.md
// Time, Order, and Clocks: Lamport and Vector Clocks
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Lamport, "Time, Clocks, and the Ordering of Events in a Distributed System" (CACM 21(7), 1978);
//          Fidge (1988) and Mattern (1989) on vector clocks. Java 21, standard library only.

import java.util.ArrayList;
import java.util.Arrays;
import java.util.Comparator;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.PriorityQueue;

/** ISO 4217 currency, copied from Phase 10 Lesson 01. Only what the ledger events in this lesson need. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);

    long minorUnitsPerMajor() {
        long scale = 1;
        for (int i = 0; i < exponent; i++) scale *= 10;
        return scale;
    }
}

/** Integer minor units plus a currency. Never a float. */
record Money(long minorUnits, Currency currency) {
    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    Money plus(Money other) {
        if (!currency.equals(other.currency)) throw new IllegalArgumentException("currency mismatch");
        return new Money(Math.addExact(minorUnits, other.minorUnits), currency);
    }

    @Override public String toString() {
        long scale = currency.minorUnitsPerMajor();
        long magnitude = Math.abs(minorUnits);
        String whole = Long.toString(magnitude / scale);
        String frac = currency.exponent() == 0 ? "" : String.format("%0" + currency.exponent() + "d", magnitude % scale);
        return (minorUnits < 0 ? "-" : "") + whole + (frac.isEmpty() ? "" : "." + frac) + " " + currency.code();
    }
}

/** How two vector clocks relate. Lamport timestamps never produce CONCURRENT: they are a total order. */
enum Relation { BEFORE, AFTER, EQUAL, CONCURRENT }

/**
 * A Lamport timestamp with the tie-break that makes it a total order: compare the counter, then the node id.
 * Two distinct events never compare equal, which is what a ledger needs to assign a single sequence.
 */
record LamportTimestamp(long time, String nodeId) implements Comparable<LamportTimestamp> {
    @Override public int compareTo(LamportTimestamp other) {
        int byTime = Long.compare(time, other.time);
        return byTime != 0 ? byTime : nodeId.compareTo(other.nodeId);
    }

    @Override public String toString() { return time + "@" + nodeId; }
}

/** Lamport's scalar clock. Three rules: tick on a local event, tick before send, max(local, remote) + 1 on receive. */
final class LamportClock {
    private long time = 0;

    long time() { return time; }

    /** Rule 1: a local event advances the clock by exactly one. */
    long tick() { return ++time; }

    /** Rule 2: sending is a local event; the value returned is what travels in the message. */
    long send() { return tick(); }

    /** Rule 3: on receive, jump past the sender's clock so the receive event is later than the send. */
    long receive(long remoteTime) {
        time = Math.max(time, remoteTime) + 1;
        return time;
    }
}

/**
 * A vector clock over a fixed cluster of n nodes. Immutable: every operation returns a new clock.
 * Component i counts the events node i has performed that this clock has observed.
 */
final class VectorClock {
    private final long[] counters;

    VectorClock(int size) { this(new long[size], true); }

    private VectorClock(long[] counters, boolean trusted) { this.counters = trusted ? counters : counters.clone(); }

    static VectorClock of(long... counters) { return new VectorClock(counters, false); }

    int size() { return counters.length; }

    long get(int node) { return counters[node]; }

    /** A local event on `node`: that component advances by one, nothing else changes. */
    VectorClock increment(int node) {
        long[] next = counters.clone();
        next[node]++;
        return new VectorClock(next, true);
    }

    /** Componentwise maximum: everything either side has observed, the result has observed. */
    VectorClock merge(VectorClock other) {
        requireSameSize(other);
        long[] next = new long[counters.length];
        for (int i = 0; i < next.length; i++) next[i] = Math.max(counters[i], other.counters[i]);
        return new VectorClock(next, true);
    }

    /** The partial order. BEFORE means this happened-before other; CONCURRENT means neither could have caused the other. */
    Relation compare(VectorClock other) {
        requireSameSize(other);
        boolean anyLess = false, anyGreater = false;
        for (int i = 0; i < counters.length; i++) {
            if (counters[i] < other.counters[i]) anyLess = true;
            if (counters[i] > other.counters[i]) anyGreater = true;
        }
        if (anyLess && anyGreater) return Relation.CONCURRENT;
        if (anyLess) return Relation.BEFORE;
        if (anyGreater) return Relation.AFTER;
        return Relation.EQUAL;
    }

    boolean happensBefore(VectorClock other) { return compare(other) == Relation.BEFORE; }

    boolean concurrentWith(VectorClock other) { return compare(other) == Relation.CONCURRENT; }

    private void requireSameSize(VectorClock other) {
        if (other.counters.length != counters.length) {
            throw new IllegalArgumentException("vector clocks must cover the same cluster: " + counters.length + " vs " + other.counters.length);
        }
    }

    @Override public boolean equals(Object o) { return o instanceof VectorClock v && Arrays.equals(counters, v.counters); }

    @Override public int hashCode() { return Arrays.hashCode(counters); }

    @Override public String toString() { return Arrays.toString(counters).replace(" ", ""); }
}

/** One thing that happened on one node, stamped by both clocks so the two can be compared side by side. */
record Event(String nodeId, String description, LamportTimestamp lamport, VectorClock vector) {
    @Override public String toString() { return String.format("%-11s %-8s %-9s %s", lamport, vector, nodeId, description); }
}

/** A message in flight. Carries the sender's Lamport time and vector clock as of the send event. */
record Message(String from, String to, String payload, long lamportTime, VectorClock vector) {}

/** One service: owns both clocks and an append-only log of its events. */
final class Node {
    final String id;
    final int index;
    final LamportClock lamport = new LamportClock();
    VectorClock vector;
    final List<Event> log = new ArrayList<>();

    Node(String id, int index, int clusterSize) {
        this.id = Objects.requireNonNull(id);
        this.index = index;
        this.vector = new VectorClock(clusterSize);
    }

    /** A local posting: both clocks tick; nothing leaves the node. */
    Event localEvent(String description) {
        long t = lamport.tick();
        vector = vector.increment(index);
        return record(description, t);
    }

    /** Sending is an event too; the message carries the clock values after the tick. */
    Message send(String to, String payload) {
        long t = lamport.send();
        vector = vector.increment(index);
        record("send " + payload + " -> " + to, t);
        return new Message(id, to, payload, t, vector);
    }

    /** Receiving merges what the sender knew, then counts the receive as a local event. */
    Event receive(Message m) {
        long t = lamport.receive(m.lamportTime());
        vector = vector.merge(m.vector()).increment(index);
        return record("recv " + m.payload() + " <- " + m.from(), t);
    }

    private Event record(String description, long lamportTime) {
        Event e = new Event(id, description, new LamportTimestamp(lamportTime, id), vector);
        log.add(e);
        return e;
    }
}

/**
 * A deterministic message scheduler. Each send is assigned a delivery tick = now + 1 + delay, where delay comes from
 * a 64-bit linear congruential generator seeded by the test. Messages are delivered in (deliveryTick, sequence) order,
 * so two messages sent in one order can arrive in the other, and the same seed always produces the same schedule.
 */
final class SimulatedNetwork {
    private record Pending(long deliverAt, long seq, Message message) {}

    private final Map<String, Node> nodes = new LinkedHashMap<>();
    private final PriorityQueue<Pending> queue = new PriorityQueue<>(
            Comparator.comparingLong(Pending::deliverAt).thenComparingLong(Pending::seq));
    private final int maxDelay;
    private long state;
    private long tick = 0;
    private long seq = 0;
    private int delivered = 0;

    SimulatedNetwork(long seed, int maxDelay) {
        if (maxDelay < 0) throw new IllegalArgumentException("maxDelay must be >= 0");
        this.maxDelay = maxDelay;
        this.state = seed;
    }

    void add(Node node) { nodes.put(node.id, node); }

    Node node(String id) {
        Node n = nodes.get(id);
        if (n == null) throw new IllegalArgumentException("unknown node " + id);
        return n;
    }

    /** Knuth's MMIX LCG constants. Same arithmetic in all three languages so the schedule is identical. */
    int nextDelay() {
        state = state * 6364136223846793005L + 1442695040888963407L;
        return (int) ((state >>> 33) % (maxDelay + 1));
    }

    void send(Message m) {
        node(m.to());
        queue.add(new Pending(tick + 1 + nextDelay(), seq++, m));
    }

    /** Deliver the earliest pending message. Returns the receive event, or null if nothing is in flight. */
    Event deliverOne() {
        Pending p = queue.poll();
        if (p == null) return null;
        tick = p.deliverAt();
        delivered++;
        return node(p.message().to()).receive(p.message());
    }

    void deliverAll() {
        while (deliverOne() != null) { /* drain */ }
    }

    int inFlight() { return queue.size(); }

    int delivered() { return delivered; }

    long tick() { return tick; }

    List<Event> allEvents() {
        List<Event> all = new ArrayList<>();
        for (Node n : nodes.values()) all.addAll(n.log);
        return all;
    }
}

public final class Main {
    static final String PAYMENTS = "payments", REFUNDS = "refunds", LEDGER = "ledger";
    static final Money CAPTURE = Money.ofMinor(4_999, Currency.USD);
    static final Money REFUND = Money.ofMinor(-4_999, Currency.USD);

    /** The single global sequence a ledger can use: Lamport time, then node id. Consistent with causality. */
    static List<Event> totalOrder(List<Event> events) {
        List<Event> sorted = new ArrayList<>(events);
        sorted.sort(Comparator.comparing(Event::lamport));
        return sorted;
    }

    /** Pairs of events that neither caused the other: the candidates for a write-write conflict. */
    static List<Event[]> concurrentPairs(List<Event> events) {
        List<Event[]> pairs = new ArrayList<>();
        for (int i = 0; i < events.size(); i++) {
            for (int j = i + 1; j < events.size(); j++) {
                if (events.get(i).vector().concurrentWith(events.get(j).vector())) {
                    pairs.add(new Event[]{events.get(i), events.get(j)});
                }
            }
        }
        return pairs;
    }

    /** Only the ledger postings: send and receive events are bookkeeping, not account updates. */
    static List<Event> postings(List<Event> events) {
        List<Event> out = new ArrayList<>();
        for (Event e : events) if (e.description().startsWith("post ")) out.add(e);
        return out;
    }

    private static SimulatedNetwork cluster(long seed) {
        SimulatedNetwork net = new SimulatedNetwork(seed, 3);
        net.add(new Node(PAYMENTS, 0, 3));
        net.add(new Node(REFUNDS, 1, 3));
        net.add(new Node(LEDGER, 2, 3));
        return net;
    }

    /**
     * The healthy flow: payments captures, tells refunds, and refunds only refunds after hearing about the capture.
     * The refund is causally after the capture, and both clocks agree on the order.
     */
    static SimulatedNetwork causalRefundScenario(long seed) {
        SimulatedNetwork net = cluster(seed);
        Node payments = net.node(PAYMENTS), refunds = net.node(REFUNDS), ledger = net.node(LEDGER);

        payments.localEvent("post captured " + CAPTURE + " pay_8f3");                 // A
        net.send(payments.send(LEDGER, "captured " + CAPTURE + " pay_8f3"));
        net.send(payments.send(REFUNDS, "captured " + CAPTURE + " pay_8f3"));
        net.deliverAll();                                                             // refunds now knows about A
        refunds.localEvent("post refunded " + REFUND + " pay_8f3");                   // B: caused by A
        net.send(refunds.send(LEDGER, "refunded " + REFUND + " pay_8f3"));
        net.deliverAll();
        ledger.localEvent("post settle pay_8f3 net " + CAPTURE.plus(REFUND));         // C: after A and B
        return net;
    }

    /**
     * The incident: an operator issues the refund from the refunds dashboard before the capture notice arrives.
     * Lamport order still produces a sequence; vector clocks reveal that the two postings are concurrent.
     */
    static SimulatedNetwork concurrentRefundScenario(long seed) {
        SimulatedNetwork net = cluster(seed);
        Node payments = net.node(PAYMENTS), refunds = net.node(REFUNDS), ledger = net.node(LEDGER);

        payments.localEvent("post captured " + CAPTURE + " pay_8f3");                 // A
        net.send(payments.send(LEDGER, "captured " + CAPTURE + " pay_8f3"));
        refunds.localEvent("post refunded " + REFUND + " pay_8f3");                   // B: concurrent with A
        net.send(refunds.send(LEDGER, "refunded " + REFUND + " pay_8f3"));
        net.deliverAll();                                                             // arrival order depends on seed
        ledger.localEvent("post settle pay_8f3 net " + CAPTURE.plus(REFUND));         // C: after A and B
        return net;
    }

    private static void printLogs(SimulatedNetwork net) {
        System.out.println(String.format("%-11s %-8s %-9s %s", "lamport", "vector", "node", "event"));
        for (String id : List.of(PAYMENTS, REFUNDS, LEDGER)) {
            for (Event e : net.node(id).log) System.out.println(e);
        }
    }

    public static void main(String[] args) {
        System.out.println("== Lamport clock: three rules ==");
        LamportClock a = new LamportClock(), b = new LamportClock();
        System.out.println("a.tick()      -> " + a.tick() + "   (local event)");
        long sent = a.send();
        System.out.println("a.send()      -> " + sent + "   (travels in the message)");
        b.tick(); b.tick(); b.tick(); b.tick(); b.tick();
        System.out.println("b is at " + b.time() + "; b.receive(" + sent + ") -> " + b.receive(sent) + "   (max(5,2)+1: local clock already ahead)");
        LamportClock c = new LamportClock();
        System.out.println("c is at 0; c.receive(" + sent + ") -> " + c.receive(sent) + "   (max(0,2)+1: jump past the sender)");

        System.out.println("\n== Vector clocks: the partial order ==");
        VectorClock v1 = VectorClock.of(2, 0, 0), v2 = VectorClock.of(0, 1, 0), v3 = v1.merge(v2).increment(1);
        System.out.println(v1 + " vs " + v2 + " -> " + v1.compare(v2));
        System.out.println(v1 + " vs " + v3 + " -> " + v1.compare(v3));
        System.out.println(v3 + " vs " + v1 + " -> " + v3.compare(v1));
        System.out.println(v3 + " = merge(" + v1 + "," + v2 + ").increment(refunds)");

        System.out.println("\n== Scenario 1: refund issued after the capture notice arrived (seed 7) ==");
        SimulatedNetwork causal = causalRefundScenario(7);
        printLogs(causal);
        Event capA = causal.node(PAYMENTS).log.get(0), refA = causal.node(REFUNDS).log.get(1);
        System.out.println("captured " + capA.vector() + " vs refunded " + refA.vector() + " -> " + capA.vector().compare(refA.vector()));

        System.out.println("\n== Scenario 2: refund issued before the capture notice arrived (seed 7) ==");
        SimulatedNetwork conc = concurrentRefundScenario(7);
        printLogs(conc);

        System.out.println("\n== Total order by (lamport, node): the sequence the ledger would assign ==");
        List<Event> events = conc.allEvents();
        for (Event e : totalOrder(events)) System.out.println(e);

        System.out.println("\n== Concurrent postings by vector clock: the conflicts to resolve ==");
        for (Event[] pair : concurrentPairs(postings(events))) {
            System.out.println(pair[0].lamport() + " " + pair[0].vector() + " '" + pair[0].description() + "'  ||  "
                    + pair[1].lamport() + " " + pair[1].vector() + " '" + pair[1].description() + "'");
        }

        System.out.println("\n== What Lamport order cannot tell you ==");
        Event captured = conc.node(PAYMENTS).log.get(0);
        Event refunded = conc.node(REFUNDS).log.get(0);
        Event first = captured.lamport().compareTo(refunded.lamport()) < 0 ? captured : refunded;
        Event second = first == captured ? refunded : captured;
        System.out.println("lamport total order puts " + first.lamport() + " before " + second.lamport()
                + "; vector clocks say " + first.vector() + " vs " + second.vector() + " is " + first.vector().compare(second.vector()));
        System.out.println("a smaller Lamport time is necessary for happens-before, never sufficient");
        System.out.println("messages delivered: " + conc.delivered() + ", final tick: " + conc.tick());
    }
}
