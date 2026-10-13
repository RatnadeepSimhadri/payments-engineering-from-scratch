// Tests for phases/05-distributed-foundations/04-physical-clocks-ntp-and-hybrid-logical-clocks.
// Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;

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

    public static void main(String[] args) {
        final long T0 = 1_700_000_000_000L;

        // drifting clock
        TrueTime t = new TrueTime(T0);
        DriftingClock exact = new DriftingClock(t, 0, 0);
        DriftingClock fast = new DriftingClock(t, 50, 0);
        DriftingClock behind = new DriftingClock(t, 0, -80);
        check("exact clock reads true time", exact.now() == T0);
        check("offset clock is behind by 80 ms", behind.error() == -80);
        t.advance(86_400_000L);
        check("50 ppm gains 4320 ms per day", fast.error() == 4320);
        check("exact clock still exact after a day", exact.error() == 0);
        fast.step(-fast.error());
        check("step corrects the offset", fast.error() == 0);
        t.advance(1_000_000);
        check("drift continues after a step", fast.error() == 50);
        checkThrows("true time never goes backwards", IllegalArgumentException.class, () -> t.advance(-1));

        // NTP arithmetic, RFC 5905 section 8
        NtpSample s = new NtpSample(1000, 1110, 1110, 1020);   // client 100 behind, 10 ms each way
        check("offset formula", s.offset() == 100);
        check("delay formula excludes server processing", s.delay() == 20);
        NtpSample busy = new NtpSample(1000, 1110, 1115, 1025); // 5 ms of server processing
        check("server processing time is excluded from delay", busy.delay() == 20);
        check("server processing does not bias offset", busy.offset() == 100);
        NtpSample asym = new NtpSample(1000, 1130, 1130, 1040); // 30 ms out, 10 ms back, client 100 behind
        check("asymmetric path biases offset by half the asymmetry", asym.offset() == 110);
        check("offset error is bounded by delay/2", Math.abs(asym.offset() - 100) <= asym.delay() / 2);

        // simulated exchange
        TrueTime t2 = new TrueTime(T0);
        DriftingClock server = new DriftingClock(t2, 0, 0);
        DriftingClock client = new DriftingClock(t2, 0, -150);
        NtpClient symmetric = new NtpClient(t2, client, server, 1, 10, 0);
        NtpSample sym = symmetric.exchange();
        check("symmetric exchange recovers the exact offset", sym.offset() == 150);
        check("symmetric exchange delay is the round trip", sym.delay() == 20);
        NtpClient jittery = new NtpClient(t2, client, server, 99, 10, 60);
        boolean allBounded = true;
        NtpSample best = null;
        for (int i = 0; i < 20; i++) {
            NtpSample x = jittery.exchange();
            if (Math.abs(x.offset() - 150) > x.delay() / 2) allBounded = false;
            if (best == null || x.delay() < best.delay()) best = x;
        }
        check("every jittery sample's error is within delay/2", allBounded);
        check("minimum-delay sample is at least as accurate as the bound promises", Math.abs(best.offset() - 150) <= best.delay() / 2);
        long correction = jittery.synchronize(16);
        check("synchronize applies a correction close to the true offset", Math.abs(correction - 150) <= 30);
        check("residual error after synchronize is small", Math.abs(client.error()) <= 30);

        // HLC timestamp packing and ordering
        HlcTimestamp a = new HlcTimestamp(T0, 3);
        HlcTimestamp b = new HlcTimestamp(T0, 4);
        HlcTimestamp c = new HlcTimestamp(T0 + 1, 0);
        check("HLC compares by l then c", a.compareTo(b) < 0 && b.compareTo(c) < 0);
        check("pack preserves order", a.pack() < b.pack() && b.pack() < c.pack());
        check("unpack round-trips", HlcTimestamp.unpack(c.pack()).equals(c) && HlcTimestamp.unpack(a.pack()).equals(a));
        checkThrows("counter must fit in 16 bits", IllegalArgumentException.class, () -> new HlcTimestamp(T0, 70_000));

        // HLC algorithm, Kulkarni et al. Figure 5
        TrueTime t3 = new TrueTime(T0);
        DriftingClock pc = new DriftingClock(t3, 0, 0);
        HybridLogicalClock hlc = new HybridLogicalClock(pc, 1000);
        HlcTimestamp e1 = hlc.now();
        HlcTimestamp e2 = hlc.now();
        check("same millisecond: l unchanged, c increments", e1.l() == T0 && e2.l() == T0 && e2.c() == e1.c() + 1);
        t3.advance(5);
        HlcTimestamp e3 = hlc.now();
        check("physical time advanced: l follows pt and c resets", e3.l() == T0 + 5 && e3.c() == 0);
        check("l is never behind physical time", e3.l() >= pc.now());
        HlcTimestamp fromFuture = new HlcTimestamp(T0 + 300, 2);
        HlcTimestamp e4 = hlc.receive(fromFuture);
        check("receive adopts a larger remote l and bumps its counter", e4.l() == T0 + 300 && e4.c() == 3);
        check("receive is after the message", e4.compareTo(fromFuture) > 0);
        HlcTimestamp e5 = hlc.now();
        check("local event after receive keeps l ahead of pt and increments c", e5.l() == T0 + 300 && e5.c() == 4);
        HlcTimestamp fromPast = new HlcTimestamp(T0 + 100, 9);
        HlcTimestamp e6 = hlc.receive(fromPast);
        check("receive from the past does not move l backwards", e6.l() == T0 + 300 && e6.c() == 5);
        HlcTimestamp tie = new HlcTimestamp(T0 + 300, 40);
        HlcTimestamp e7 = hlc.receive(tie);
        check("equal l on both sides: c = max(c, c_m) + 1", e7.l() == T0 + 300 && e7.c() == 41);
        t3.advance(1000);
        HlcTimestamp e8 = hlc.now();
        check("once pt passes l, l rejoins pt and c resets", e8.l() == T0 + 1005 && e8.c() == 0);
        checkThrows("remote timestamp beyond the drift bound is rejected", ClockDriftException.class,
                () -> hlc.receive(new HlcTimestamp(pc.now() + 5000, 0)));
        check("rejected receive did not corrupt the clock", hlc.peek().l() == T0 + 1005);

        // the payment trace: causality respected by Lamport and HLC, violated by wall clocks
        TrueTime t4 = new TrueTime(T0);
        Node gateway = new Node("gateway", t4, 0, 0, 1000);
        Node ledger = new Node("ledger", t4, 0, -80, 1000);
        Node notifier = new Node("notifier", t4, 0, 35, 1000);
        Trace trace = Main.paymentTrace(t4, gateway, ledger, notifier);
        TraceReport r = trace.report();
        check("trace has four causal pairs", r.pairs() == 4);
        check("wall clocks violate causality on the skewed node", r.wallViolations() > 0);
        check("Lamport never violates causality", r.lamportViolations() == 0);
        check("HLC never violates causality", r.hlcViolations() == 0);
        check("HLC stays within the clock skew of true time", r.maxHlcAheadOfTrueMs() <= 35);
        boolean hlcMonotonicPerNode = true;
        HlcTimestamp lastLedger = null;
        for (TraceEvent e : trace.events) {
            if (e.node().equals("ledger")) {
                if (lastLedger != null && e.hlc().compareTo(lastLedger) <= 0) hlcMonotonicPerNode = false;
                lastLedger = e.hlc();
            }
        }
        check("HLC is strictly monotonic on one node", hlcMonotonicPerNode);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
