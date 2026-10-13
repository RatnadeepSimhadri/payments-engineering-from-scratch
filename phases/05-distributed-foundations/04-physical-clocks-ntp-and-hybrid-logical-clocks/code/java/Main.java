// Lesson: phases/05-distributed-foundations/04-physical-clocks-ntp-and-hybrid-logical-clocks/docs/en.md
// Physical Clocks, NTP, and Hybrid Logical Clocks
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: RFC 5905 (NTPv4) section 8 for the offset/delay equations; Kulkarni, Demirbas, Madeppa, Avva, Leone,
//          "Logical Physical Clocks" (OPODIS 2014) for the HLC algorithm; Lamport 1978 for logical clocks. Java 21, stdlib only.

import java.util.ArrayList;
import java.util.List;
import java.util.Random;

/**
 * The one true time of the simulation, in milliseconds. Nothing in the lesson reads System.currentTimeMillis();
 * every clock derives its reading from this value, so every run is deterministic and replayable.
 */
final class TrueTime {
    private long nowMs;

    TrueTime(long startMs) { this.nowMs = startMs; }

    long now() { return nowMs; }

    void advance(long ms) {
        if (ms < 0) throw new IllegalArgumentException("time does not go backwards");
        nowMs += ms;
    }
}

/**
 * A physical clock on one machine: true time distorted by a constant rate error (drift, parts per million)
 * and an additive offset. A quartz oscillator drifts 10 to 100 ppm; a 50 ppm clock gains 4.3 s per day.
 * {@link #step} models NTP correcting the offset; the drift stays, which is why correction is never finished.
 */
final class DriftingClock {
    private final TrueTime trueTime;
    private final double driftPpm;
    private long offsetMs;
    private final long epochTrueMs;

    DriftingClock(TrueTime trueTime, double driftPpm, long initialOffsetMs) {
        this.trueTime = trueTime;
        this.driftPpm = driftPpm;
        this.offsetMs = initialOffsetMs;
        this.epochTrueMs = trueTime.now();
    }

    /** What this machine believes the time is. Rounded to whole milliseconds like a real clock read. */
    long now() {
        long elapsed = trueTime.now() - epochTrueMs;
        long drifted = Math.round(elapsed * (driftPpm / 1_000_000.0));
        return epochTrueMs + elapsed + drifted + offsetMs;
    }

    /** Apply a correction, as NTP does when the offset is large: the clock jumps. Negative deltas move it backwards. */
    void step(long deltaMs) { offsetMs += deltaMs; }

    long error() { return now() - trueTime.now(); }
}

/** One NTP exchange, RFC 5905 section 8: four timestamps, two derived quantities. */
record NtpSample(long t1, long t2, long t3, long t4) {
    /** How far the client clock is behind the server (positive means client is behind). */
    long offset() { return ((t2 - t1) + (t3 - t4)) / 2; }

    /** Round-trip delay excluding the server's processing time. The offset error is at most delay/2. */
    long delay() { return (t4 - t1) - (t3 - t2); }
}

/**
 * A client that polls a server over a network with seeded, possibly asymmetric latency and keeps the
 * sample with the smallest delay, which is NTP's clock-filter rule: the fastest round trip is the one
 * least contaminated by queueing, so its offset is the most trustworthy.
 */
final class NtpClient {
    private final TrueTime trueTime;
    private final DriftingClock client;
    private final DriftingClock server;
    private final Random random;
    private final long minLatencyMs;
    private final long jitterMs;

    NtpClient(TrueTime trueTime, DriftingClock client, DriftingClock server, long seed, long minLatencyMs, long jitterMs) {
        this.trueTime = trueTime;
        this.client = client;
        this.server = server;
        this.random = new Random(seed);
        this.minLatencyMs = minLatencyMs;
        this.jitterMs = jitterMs;
    }

    /** One request/response with independently drawn outbound and return latencies. */
    NtpSample exchange() {
        long outbound = minLatencyMs + (jitterMs == 0 ? 0 : random.nextLong(jitterMs + 1));
        long inbound = minLatencyMs + (jitterMs == 0 ? 0 : random.nextLong(jitterMs + 1));
        long t1 = client.now();
        trueTime.advance(outbound);
        long t2 = server.now();
        long t3 = server.now();              // zero processing time keeps the arithmetic legible
        trueTime.advance(inbound);
        long t4 = client.now();
        return new NtpSample(t1, t2, t3, t4);
    }

    /** Poll n times and return the sample with the smallest delay (ties: earliest). */
    NtpSample bestOf(int n) {
        NtpSample best = null;
        for (int i = 0; i < n; i++) {
            NtpSample s = exchange();
            if (best == null || s.delay() < best.delay()) best = s;
        }
        return best;
    }

    /** Measure and correct the client clock by the best offset estimate. Returns the applied correction. */
    long synchronize(int polls) {
        NtpSample best = bestOf(polls);
        client.step(best.offset());
        return best.offset();
    }
}

/** A Lamport timestamp: ordering with no relation to physical time. Here only for the comparison trace. */
final class LamportClock {
    private long time = 0;

    long tick() { return ++time; }

    long receive(long remote) {
        time = Math.max(time, remote) + 1;
        return time;
    }

    long peek() { return time; }
}

/**
 * HLC timestamp: l is the logical wall time (never behind the physical clock, never more than epsilon ahead),
 * c is a counter that breaks ties among events with the same l. Compared lexicographically.
 * Packs into 64 bits: 48 for l (enough for 8,900 years of milliseconds) and 16 for c (65,535 events per ms).
 */
record HlcTimestamp(long l, int c) implements Comparable<HlcTimestamp> {
    static final int COUNTER_BITS = 16;
    static final long COUNTER_MAX = (1L << COUNTER_BITS) - 1;

    HlcTimestamp {
        if (c < 0 || c > COUNTER_MAX) throw new IllegalArgumentException("counter out of range: " + c);
        if (l < 0 || l >= (1L << 48)) throw new IllegalArgumentException("logical time out of range: " + l);
    }

    long pack() { return (l << COUNTER_BITS) | c; }

    static HlcTimestamp unpack(long packed) {
        return new HlcTimestamp(packed >>> COUNTER_BITS, (int) (packed & COUNTER_MAX));
    }

    @Override
    public int compareTo(HlcTimestamp o) {
        int byL = Long.compare(l, o.l);
        return byL != 0 ? byL : Integer.compare(c, o.c);
    }

    @Override
    public String toString() { return l + "." + c; }
}

/** Thrown when the physical clock has drifted so far that l would run more than maxDriftMs ahead of it. */
final class ClockDriftException extends RuntimeException {
    ClockDriftException(long l, long pt, long maxDrift) {
        super("logical time " + l + " is " + (l - pt) + " ms ahead of physical time " + pt + "; bound is " + maxDrift);
    }
}

/**
 * Hybrid Logical Clock, Kulkarni et al. 2014, Figure 5. Each node holds (l, c). Two rules:
 *  - send/local: l' = max(l, pt); c' = (l' == l) ? c + 1 : 0
 *  - receive(m): l' = max(l, m.l, pt); c' depends on which of the three l's won the max (see receive())
 * Invariants: l >= pt always (never behind physical time); l - pt <= maxDrift (never far ahead);
 * e happens-before f implies timestamp(e) < timestamp(f).
 */
final class HybridLogicalClock {
    private final DriftingClock physical;
    private final long maxDriftMs;
    private long l;
    private int c;

    HybridLogicalClock(DriftingClock physical, long maxDriftMs) {
        this.physical = physical;
        this.maxDriftMs = maxDriftMs;
        this.l = physical.now();
        this.c = 0;
    }

    /** Timestamp a local event or an outgoing message. */
    HlcTimestamp now() {
        long pt = physical.now();
        long newL = Math.max(l, pt);
        int newC = (newL == l) ? bumpCounter(c) : 0;
        return commit(newL, newC, pt);
    }

    /** Merge an incoming message's timestamp, then timestamp the receive event. */
    HlcTimestamp receive(HlcTimestamp m) {
        long pt = physical.now();
        long newL = Math.max(Math.max(l, m.l()), pt);
        int newC;
        if (newL == l && newL == m.l()) newC = bumpCounter(Math.max(c, m.c()));
        else if (newL == l) newC = bumpCounter(c);
        else if (newL == m.l()) newC = bumpCounter(m.c());
        else newC = 0;
        return commit(newL, newC, pt);
    }

    HlcTimestamp peek() { return new HlcTimestamp(l, c); }

    private static int bumpCounter(int c) {
        if (c >= HlcTimestamp.COUNTER_MAX) throw new ArithmeticException("HLC counter overflow: more than 65535 events in one millisecond");
        return c + 1;
    }

    /** Validate before mutating, so a rejected timestamp leaves the clock exactly as it was. */
    private HlcTimestamp commit(long newL, int newC, long pt) {
        if (newL - pt > maxDriftMs) throw new ClockDriftException(newL, pt, maxDriftMs);
        l = newL;
        c = newC;
        return new HlcTimestamp(l, c);
    }
}

/** One stamped event in the comparison trace. */
record TraceEvent(String node, String what, long trueMs, long wallMs, long lamport, HlcTimestamp hlc) {}

/**
 * A node with all three clocks, so the same event can be stamped three ways and the stamps compared.
 */
final class Node {
    final String name;
    final DriftingClock wall;
    final LamportClock lamport = new LamportClock();
    final HybridLogicalClock hlc;
    private final TrueTime trueTime;

    Node(String name, TrueTime trueTime, double driftPpm, long offsetMs, long maxDriftMs) {
        this.name = name;
        this.trueTime = trueTime;
        this.wall = new DriftingClock(trueTime, driftPpm, offsetMs);
        this.hlc = new HybridLogicalClock(wall, maxDriftMs);
    }

    TraceEvent local(String what) {
        return new TraceEvent(name, what, trueTime.now(), wall.now(), lamport.tick(), hlc.now());
    }

    TraceEvent send(String what) { return local("send " + what); }

    TraceEvent receive(String what, TraceEvent message) {
        return new TraceEvent(name, "recv " + what, trueTime.now(), wall.now(),
                lamport.receive(message.lamport()), hlc.receive(message.hlc()));
    }
}

/** Causal pairs (send, receive) and the three orderings' verdicts on them. */
record TraceReport(int pairs, int wallViolations, int lamportViolations, int hlcViolations, long maxHlcAheadOfTrueMs) {}

final class Trace {
    final List<TraceEvent> events = new ArrayList<>();
    final List<TraceEvent[]> causalPairs = new ArrayList<>();

    TraceEvent record(TraceEvent e) { events.add(e); return e; }

    TraceEvent deliver(Node to, String what, TraceEvent sent) {
        TraceEvent received = record(to.receive(what, sent));
        causalPairs.add(new TraceEvent[]{sent, received});
        return received;
    }

    TraceReport report() {
        int wall = 0, lamport = 0, hlc = 0;
        for (TraceEvent[] p : causalPairs) {
            if (!(p[0].wallMs() < p[1].wallMs())) wall++;
            if (!(p[0].lamport() < p[1].lamport())) lamport++;
            if (!(p[0].hlc().compareTo(p[1].hlc()) < 0)) hlc++;
        }
        long maxAhead = 0;
        for (TraceEvent e : events) maxAhead = Math.max(maxAhead, e.hlc().l() - e.trueMs());
        return new TraceReport(causalPairs.size(), wall, lamport, hlc, maxAhead);
    }
}

public final class Main {

    /** Three services, one payment: gateway authorises, ledger posts, notifier emails. Clocks disagree by up to 80 ms. */
    static Trace paymentTrace(TrueTime t, Node gateway, Node ledger, Node notifier) {
        Trace trace = new Trace();
        TraceEvent auth = trace.record(gateway.send("authorize pay_7f3"));
        t.advance(4);
        TraceEvent posted = trace.deliver(ledger, "authorize pay_7f3", auth);
        t.advance(2);
        TraceEvent postAck = trace.record(ledger.send("posted pay_7f3"));
        t.advance(3);
        trace.deliver(gateway, "posted pay_7f3", postAck);
        t.advance(1);
        TraceEvent capture = trace.record(gateway.send("capture pay_7f3"));
        t.advance(5);
        TraceEvent captured = trace.deliver(ledger, "capture pay_7f3", capture);
        t.advance(2);
        TraceEvent notify = trace.record(ledger.send("notify pay_7f3"));
        t.advance(6);
        trace.deliver(notifier, "notify pay_7f3", notify);
        t.advance(1);
        trace.record(notifier.local("email sent"));
        return trace;
    }

    public static void main(String[] args) {
        System.out.println("== A quartz clock drifts ==");
        TrueTime t = new TrueTime(1_700_000_000_000L);
        DriftingClock fast = new DriftingClock(t, 50, 0);
        t.advance(86_400_000L);
        System.out.printf("50 ppm clock after one day: %d ms fast%n", fast.error());

        System.out.println("\n== NTP: estimating offset from four timestamps ==");
        TrueTime t2 = new TrueTime(1_700_000_000_000L);
        DriftingClock server = new DriftingClock(t2, 0, 0);
        DriftingClock client = new DriftingClock(t2, 20, -150);
        NtpClient ntp = new NtpClient(t2, client, server, 42, 10, 0);
        NtpSample symmetric = ntp.exchange();
        System.out.printf("symmetric 10 ms each way: offset=%d ms (true -150), delay=%d ms%n", symmetric.offset(), symmetric.delay());
        NtpClient jittery = new NtpClient(t2, client, server, 7, 10, 40);
        for (int i = 0; i < 3; i++) {
            NtpSample s = jittery.exchange();
            System.out.printf("jittery sample %d: offset=%d ms, delay=%d ms, error=%d ms (bound delay/2=%d)%n",
                    i + 1, s.offset(), s.delay(), s.offset() - (-client.error()), s.delay() / 2);
        }
        long applied = jittery.synchronize(8);
        System.out.printf("best-of-8 correction applied: %d ms; residual client error after step: %d ms%n", applied, client.error());

        System.out.println("\n== Three clocks on one payment trace ==");
        TrueTime t3 = new TrueTime(1_700_000_000_000L);
        Node gateway = new Node("gateway", t3, 0, 0, 1000);
        Node ledger = new Node("ledger", t3, 0, -80, 1000);     // ledger's wall clock is 80 ms behind
        Node notifier = new Node("notifier", t3, 0, 35, 1000);
        Trace trace = paymentTrace(t3, gateway, ledger, notifier);
        final long T0 = 1_700_000_000_000L;
        System.out.printf("%-9s %-22s %-8s %-8s %-8s %s%n", "node", "event", "true", "wall", "lamport", "hlc (l shown relative to T0)");
        for (TraceEvent e : trace.events) {
            System.out.printf("%-9s %-22s %-8d %-8d %-8d %d.%d%n", e.node(), e.what(), e.trueMs() - T0, e.wallMs() - T0,
                    e.lamport(), e.hlc().l() - T0, e.hlc().c());
        }
        TraceReport r = trace.report();
        System.out.printf("causal pairs: %d; wall-clock order violated: %d; Lamport violated: %d; HLC violated: %d%n",
                r.pairs(), r.wallViolations(), r.lamportViolations(), r.hlcViolations());
        System.out.printf("HLC stayed within %d ms of true time; Lamport's largest value was %d (unrelated to time)%n",
                r.maxHlcAheadOfTrueMs(), trace.events.get(trace.events.size() - 1).lamport());

        System.out.println("\n== Bounded drift: HLC refuses to run away from physical time ==");
        TrueTime t4 = new TrueTime(1_700_000_000_000L);
        DriftingClock slow = new DriftingClock(t4, 0, 0);
        HybridLogicalClock hlc = new HybridLogicalClock(slow, 500);
        try {
            hlc.receive(new HlcTimestamp(slow.now() + 2_000, 0));
        } catch (ClockDriftException e) {
            System.out.println("rejected: " + e.getMessage());
        }
        HlcTimestamp packed = new HlcTimestamp(1_700_000_000_123L, 7);
        System.out.printf("pack(%s) = %d, unpack round-trips: %s%n", packed, packed.pack(), HlcTimestamp.unpack(packed.pack()));
    }
}
