// Tests for phases/14-payments-at-scale/10-simulation-testing-of-payment-systems. Run: javac -d .out *.java && java -cp .out MainTest
// Everything is deterministic: no wall clock, no threads, no network. A failing check names the invariant it broke.
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
        // --- deterministic primitives ---
        check("splitmix64 seed 0 first draw matches the reference vector", new Rng(0).nextLong() == 0xe220a8397b1dcdafL);
        check("splitmix64 seed 0 second draw matches the reference vector", secondDraw(0) == 0x6e789e6aa1b965f4L);
        Rng bounded = new Rng(42);
        boolean inRange = true;
        for (int i = 0; i < 10_000; i++) { int x = bounded.nextInt(7); if (x < 0 || x >= 7) inRange = false; }
        check("nextInt stays inside [0, bound)", inRange);
        check("chance(0) never fires and chance(10000) always fires", !new Rng(1).chance(0) && new Rng(1).chance(10_000));
        check("nextLongBounded handles bounds beyond 32 bits", new Rng(3).nextLongBounded(10_000_000_000L) < 10_000_000_000L);
        checkThrows("nextInt rejects a non-positive bound", IllegalArgumentException.class, () -> new Rng(1).nextInt(0));
        Rng s1 = new Rng(7), s2 = new Rng(7);
        check("two Rngs with one seed produce the same stream", s1.nextLong() == s2.nextLong() && s1.nextInt(100) == s2.nextInt(100));

        // --- virtual clock: ordering by (time, seq) and no real sleeping ---
        Simulator sim = new Simulator();
        List<String> order = new ArrayList<>();
        sim.schedule(50, "b", () -> order.add("b"));
        sim.schedule(10, "a", () -> order.add("a"));
        sim.schedule(50, "c", () -> order.add("c"));
        sim.runUntilIdle(100);
        check("events run in time order, ties in scheduling order", order.equals(List.of("a", "b", "c")) && sim.now == 50);
        Simulator idle = new Simulator();
        idle.runUntil(5_000);
        check("runUntil advances the clock even with nothing scheduled", idle.now == 5_000);
        checkThrows("schedule rejects negative delays", IllegalArgumentException.class, () -> new Simulator().schedule(-1, "x", () -> { }));
        Simulator loop = new Simulator();
        Runnable[] self = new Runnable[1];
        self[0] = () -> loop.schedule(1, "again", self[0]);
        loop.schedule(0, "again", self[0]);
        checkThrows("runUntilIdle turns an infinite loop into a failure", IllegalStateException.class, () -> loop.runUntilIdle(1_000));

        // --- simulated network: latency, drops, partitions ---
        Simulator nsim = new Simulator();
        Network reliable = new Network(nsim, new Rng(1), new NetworkConfig(20, 200, 0));
        boolean[] got = {false};
        reliable.send("client-0", "gateway", "ping", () -> got[0] = true);
        nsim.runUntilIdle(10);
        check("a message on a lossless link is delivered within the latency window", got[0] && nsim.now >= 20 && nsim.now <= 200);
        reliable.partition("client-0", "gateway");
        boolean[] got2 = {false};
        reliable.send("gateway", "client-0", "pong", () -> got2[0] = true);
        nsim.runUntilIdle(10);
        check("a partition blocks both directions and counts the message as blocked", !got2[0] && reliable.blocked == 1);
        reliable.heal("client-0", "gateway");
        check("heal removes the partition", !reliable.isPartitioned("client-0", "gateway") && !reliable.isPartitioned("gateway", "client-0"));
        Network lossy = new Network(new Simulator(), new Rng(5), new NetworkConfig(1, 1, 10_000));
        lossy.send("a", "b", "x", () -> { });
        check("a 100% drop rate drops every message", lossy.dropped == 1 && lossy.delivered == 0);

        // --- money and ledger discipline ---
        Money ten = Money.ofMinor(1000, Currency.USD);
        checkThrows("money refuses to mix currencies", CurrencyMismatchException.class, () -> ten.plus(Money.ofMinor(1, new Currency("EUR", 2))));
        checkThrows("transfer rejects a non-positive amount", IllegalArgumentException.class,
                () -> JournalEntry.transfer("e1", "a", "b", Money.zero(Currency.USD)));
        Ledger ledger = new Ledger(Currency.USD);
        ledger.post(JournalEntry.transfer("pay-1", "asset:receivable:c1", "liability:clearing", ten));
        check("a transfer leaves the trial balance at zero", ledger.trialBalance().isZero() && ledger.balance("asset:receivable:c1").equals(ten));
        checkThrows("ledger rejects an unbalanced entry", IllegalArgumentException.class,
                () -> ledger.post(new JournalEntry("bad", List.of(new Posting("a", ten)))));

        // --- processor idempotency on authKey ---
        Simulator psim = new Simulator();
        Network pnet = new Network(psim, new Rng(2), new NetworkConfig(1, 1, 0));
        Processor processor = new Processor(psim, pnet, new Rng(3), 0);
        Gateway gw = new Gateway(psim, pnet, processor, Defect.NONE, 3_000, 2_000);
        AuthRequest req = new AuthRequest("auth-pay-1", "pay-1", "c1", ten, 1);
        processor.receive(req, gw);
        processor.receive(new AuthRequest("auth-pay-1", "pay-1", "c1", ten, 2), gw);
        check("the processor decides once per authKey and replays the retry", processor.approvals.size() == 1 && processor.replayed == 1);

        // --- gateway idempotency on the client key ---
        Simulator gsim = new Simulator();
        Network gnet = new Network(gsim, new Rng(4), new NetworkConfig(1, 1, 0));
        Processor proc2 = new Processor(gsim, gnet, new Rng(5), 0);
        Gateway gw2 = new Gateway(gsim, gnet, proc2, Defect.NONE, 3_000, 2_000);
        List<Status> replies = new ArrayList<>();
        gw2.handle(new PaymentRequest("key-1", "c1", ten), replies::add);
        gw2.handle(new PaymentRequest("key-1", "c1", ten), replies::add);
        gsim.runUntilIdle(1_000);
        check("one key makes one payment; the duplicate waits and gets the same answer",
                gw2.payments.size() == 1 && gw2.replays == 1 && replies.equals(List.of(Status.APPROVED, Status.APPROVED)));
        check("the approval was posted exactly once", gw2.ledger.countEntries("pay-000001") == 1 && gw2.ledger.trialBalance().isZero());

        // --- crash-when-busy fires in the dangerous window ---
        Simulator csim = new Simulator();
        Network cnet = new Network(csim, new Rng(6), new NetworkConfig(1, 1, 0));
        Processor proc3 = new Processor(csim, cnet, new Rng(7), 0);
        Gateway gw3 = new Gateway(csim, cnet, proc3, Defect.NONE, 3_000, 2_000);
        gw3.crashWhenBusy();
        check("an armed crash does not fire while nothing is pending", gw3.crashes == 0);
        proc3.down = true;                                                  // the first auth attempt is swallowed
        csim.schedule(1_000, "processor-up", () -> proc3.down = false);
        List<Status> late = new ArrayList<>();
        gw3.handle(new PaymentRequest("key-9", "c9", ten), late::add);
        check("the armed crash fires once a payment is in flight", gw3.crashes == 1 && late.isEmpty());
        csim.runUntilIdle(1_000);
        check("recovery re-drives the in-flight payment and posts it once",
                gw3.payments.get("pay-000001").status == Status.APPROVED && gw3.recovered == 1 && gw3.ledger.countEntries("pay-000001") == 1);
        check("the timer armed before the crash was invalidated by the epoch", gw3.authTimeouts == 0 && gw3.authSends == 2);

        // --- fault schedule ---
        FaultSchedule schedule = FaultSchedule.generate(new Rng(11), 3000L * Harness.HOUR, 100);
        boolean sorted = true, late2 = false, inside = true;
        for (int i = 1; i < schedule.faults.size(); i++) if (schedule.faults.get(i - 1).startMs() > schedule.faults.get(i).startMs()) sorted = false;
        for (Fault f : schedule.faults) {
            if (f.startMs() > Integer.MAX_VALUE) late2 = true;
            if (f.startMs() < 0 || f.endMs() > 3000L * Harness.HOUR || f.endMs() < f.startMs()) inside = false;
        }
        check("faults are sorted, inside the horizon, and reach beyond 2^31 ms on long horizons", sorted && late2 && inside);

        // --- the whole system: determinism, invariants, planted defects ---
        SimReport a = Harness.run(SimConfig.standard(1, 48));
        SimReport b = Harness.run(SimConfig.standard(1, 48));
        check("same seed gives the same fingerprint and the same counts", a.fingerprint().equals(b.fingerprint()) && a.events() == b.events() && a.payments() == b.payments());
        check("a different seed gives a different fingerprint", !a.fingerprint().equals(Harness.run(SimConfig.standard(2, 48)).fingerprint()));
        check("the correct gateway survives 48 hours of faults with no violation", a.passed() && a.pending() == 0 && a.payments() > 500);
        check("the run actually exercised faults", a.crashes() > 0 && a.netDropped() > 0 && a.authTimeouts() > 0);
        check("trial balance is zero at the end", a.trialBalance().isZero());
        SimReport fresh = Harness.findFailingSeed(SimConfig.standard(1, 24).with(Defect.FRESH_AUTH_KEY_ON_RETRY), 1, 20);
        check("a fresh auth key per retry is caught as a double authorization", fresh != null && fresh.firstBrokenInvariant().equals("no-double-authorization"));
        SimReport skip = Harness.findFailingSeed(SimConfig.standard(1, 24).with(Defect.RECOVERY_SKIPS_PENDING), 1, 20);
        check("skipping recovery is caught as a stuck or unposted payment",
                skip != null && (skip.firstBrokenInvariant().equals("no-stuck-payments") || skip.firstBrokenInvariant().equals("approval-posted-once")));
        check("the failing report carries the seed to replay it", skip != null && skip.reproduction().contains("SimConfig.standard(" + skip.seed() + ", 24)"));
        check("a passing search returns null", Harness.findFailingSeed(SimConfig.standard(1, 12), 1, 2) == null);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }

    private static long secondDraw(long seed) {
        Rng r = new Rng(seed);
        r.nextLong();
        return r.nextLong();
    }
}
