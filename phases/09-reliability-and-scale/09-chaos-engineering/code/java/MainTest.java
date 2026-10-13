// Tests for phases/09-reliability-and-scale/09-chaos-engineering. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.Set;

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

    /** A fresh, fully seeded world. Same seeds => same results, which is what makes chaos replayable. */
    record World(SimClock clock, FaultInjector injector, Processor processor, SimNetwork network, Gateway gateway,
                 TrafficGenerator traffic, ExperimentRunner runner) {
        static World create() {
            SimClock clock = new SimClock(1_700_000_000_000L);
            FaultInjector injector = new FaultInjector(7);
            Processor processor = new Processor(clock, 11);
            SimNetwork network = new SimNetwork(clock, injector, 13);
            Gateway gateway = new Gateway(clock, network, processor);
            TrafficGenerator traffic = new TrafficGenerator(17, "pay");
            return new World(clock, injector, processor, network, gateway, traffic,
                    new ExperimentRunner(clock, injector, gateway, processor, traffic));
        }
    }

    static Experiment exp(String target, Fault fault, BlastRadius radius) {
        return new Experiment("t", "h", target, fault, radius, Main.STEADY, Main.ABORT, 300);
    }

    public static void main(String[] args) {
        Money amount = Money.ofMinor(1999, Currency.USD);

        // --- value types and the injector itself
        check("money prints minor units with currency", amount.toString().equals("19.99 USD"));
        checkThrows("negative latency fault rejected", IllegalArgumentException.class, () -> new Fault.Latency(-1));
        checkThrows("blast radius percent bounded", IllegalArgumentException.class, () -> new BlastRadius.PercentOfRequests(101));
        checkThrows("clock never moves backwards", IllegalArgumentException.class, () -> new SimClock(0).advance(-5));
        FaultInjector inj = new FaultInjector(1);
        check("injector starts disarmed", !inj.isArmed());
        inj.inject("processor", new Fault.Error("HTTP_500"), new BlastRadius.Everyone());
        AuthRequest req = new AuthRequest("p1", "m-1", amount, "idem-p1", 0);
        check("injector matches target", inj.faultsFor("processor", req).size() == 1);
        check("injector ignores other targets", inj.faultsFor("gateway->processor", req).isEmpty());
        inj.clear();
        check("clear disarms and later requests see nothing", !inj.isArmed() && inj.faultsFor("processor", req).isEmpty());
        check("merchant blast radius is exact", new BlastRadius.Merchants(Set.of("m-1")).includes(req, null)
                && !new BlastRadius.Merchants(Set.of("m-2")).includes(req, null));

        // --- happy path: no faults, the gateway authorizes in one attempt
        World w = World.create();
        AuthResult ok = w.gateway().authorize("p-1", "m-0", amount);
        check("no fault: one attempt", ok.attempts() == 1);
        check("no fault: latency within baseline band", ok.latencyMillis() >= 80 && ok.latencyMillis() <= 120);
        check("no fault: good outcome", ok.outcome().isGood());

        // --- latency beyond the timeout: retry with the same idempotency key, no double authorization
        w = World.create();
        w.injector().inject(SimNetwork.LINK_GATEWAY_TO_PROCESSOR, new Fault.Latency(2_500), new BlastRadius.Everyone());
        AuthResult slow = w.gateway().authorize("p-2", "m-0", amount);
        check("latency beyond timeout: both attempts time out", slow.outcome() == Outcome.TIMEOUT && slow.attempts() == 2);
        check("latency beyond timeout: latency is 2 x timeout", slow.latencyMillis() == 2 * Gateway.TIMEOUT_MILLIS);
        check("latency beyond timeout: nothing authorized", w.processor().authorizationsFor("p-2") == 0);

        // --- processor error then recovery: the retry must not create a second authorization
        w = World.create();
        Processor p = w.processor();
        AuthRequest first = new AuthRequest("p-3", "m-0", amount, "idem-p-3", w.clock().nowMillis());
        Outcome o1 = p.handle(first);
        Outcome o2 = p.handle(first);
        check("processor replays the same answer for the same idempotency key", o1 == o2);
        check("processor records at most one authorization per payment", p.authorizationsFor("p-3") <= 1 && p.noDoubleAuthorizations());

        // --- partition looks like a timeout from the caller's seat
        w = World.create();
        w.injector().inject(SimNetwork.LINK_GATEWAY_TO_PROCESSOR, new Fault.Partition(), new BlastRadius.Merchants(Set.of("m-3")));
        AuthResult cut = w.gateway().authorize("p-4", "m-3", amount);
        AuthResult fine = w.gateway().authorize("p-5", "m-4", amount);
        check("partition: canary merchant times out", cut.outcome() == Outcome.TIMEOUT);
        check("partition: other merchant unaffected", fine.outcome().isGood() && fine.attempts() == 1);

        // --- clock skew beyond the signing window is rejected; inside it is accepted
        w = World.create();
        w.injector().inject(SimNetwork.TARGET_PROCESSOR, new Fault.ClockSkew(6 * 60 * 1000L), new BlastRadius.Everyone());
        check("skew of 6 minutes: stale request", w.gateway().authorize("p-6", "m-0", amount).outcome() == Outcome.STALE_REQUEST);
        w.injector().clear();
        w.injector().inject(SimNetwork.TARGET_PROCESSOR, new Fault.ClockSkew(4 * 60 * 1000L), new BlastRadius.Everyone());
        check("skew of 4 minutes: accepted", w.gateway().authorize("p-7", "m-0", amount).outcome().isGood());
        check("skew is not retried (not a transient fault)", w.gateway().authorize("p-8", "m-0", amount).attempts() == 1);

        // --- metrics: p99 is a quantile, not an average
        Metrics m = new Metrics();
        for (int i = 1; i <= 100; i++) m.record(new AuthResult(Outcome.AUTHORIZED, i, 1));
        check("p99 of 1..100 is 99", m.p99Millis() == 99);
        m.record(new AuthResult(Outcome.TIMEOUT, 4_000, 2));
        check("good rate counts timeouts as bad", m.goodRatePercent() < 100.0 && m.goodRatePercent() > 99.0);
        check("empty metrics: 100% good", new Metrics().goodRatePercent() == 100.0);

        // --- experiments: hypothesis held, hypothesis violated, and abort
        w = World.create();
        ExperimentReport held = w.runner().run(exp(SimNetwork.LINK_GATEWAY_TO_PROCESSOR, new Fault.Latency(300), new BlastRadius.PercentOfRequests(10)));
        check("300ms on 10%: baseline established", held.baselineHeld());
        check("300ms on 10%: hypothesis held", held.hypothesisHeld() && !held.aborted());
        check("300ms on 10%: p99 rose but stayed under the SLO", held.experimental().p99Millis() > held.baseline().p99Millis()
                && held.experimental().p99Millis() <= Main.STEADY.maxP99Millis());

        ExperimentReport aborted = w.runner().run(exp(SimNetwork.TARGET_PROCESSOR, new Fault.Error("HTTP_500"), new BlastRadius.PercentOfRequests(70)));
        check("500s on 70%: abort fired", aborted.aborted());
        check("500s on 70%: abort fired at the first checkpoint", aborted.requestsBeforeAbort() == Main.ABORT.checkEvery());
        check("500s on 70%: faults cleared after abort", !w.injector().isArmed());
        check("500s on 70%: system recovered", aborted.recovered());
        check("500s on 70%: verdict mentions ABORTED", aborted.verdict().startsWith("ABORTED"));

        ExperimentReport skewed = w.runner().run(exp(SimNetwork.TARGET_PROCESSOR, new Fault.ClockSkew(6 * 60 * 1000L), new BlastRadius.Everyone()));
        check("skew experiment: aborted, the processor rejected everything", skewed.aborted() && skewed.experimental().count(Outcome.STALE_REQUEST) > 0);
        check("skew experiment: invariant held throughout", skewed.noDoubleAuthorizations());

        ExperimentReport partition = w.runner().run(exp(SimNetwork.LINK_GATEWAY_TO_PROCESSOR, new Fault.Partition(), new BlastRadius.Merchants(Set.of("m-3"))));
        check("partition on one merchant: not aborted (good rate stays above 80%)", !partition.aborted());
        check("partition on one merchant: hypothesis violated via p99", !partition.hypothesisHeld()
                && partition.experimental().p99Millis() > Main.STEADY.maxP99Millis());
        check("partition on one merchant: no double authorization", partition.noDoubleAuthorizations());

        // --- a steady state that cannot be met means the experiment is not run
        w = World.create();
        Experiment impossible = new Experiment("x", "h", SimNetwork.TARGET_PROCESSOR, new Fault.Error("E"), new BlastRadius.Everyone(),
                new SteadyState(100.0, 50), Main.ABORT, 100);
        ExperimentReport notRun = w.runner().run(impossible);
        check("impossible steady state: not run", !notRun.baselineHeld() && notRun.verdict().startsWith("NOT RUN"));
        check("impossible steady state: injector never armed", !w.injector().isArmed() && w.injector().appliedCount() == 0);

        // --- determinism: two identical worlds produce identical reports
        ExperimentReport a = World.create().runner().run(exp(SimNetwork.LINK_GATEWAY_TO_PROCESSOR, new Fault.Latency(2_500), new BlastRadius.PercentOfRequests(10)));
        ExperimentReport b = World.create().runner().run(exp(SimNetwork.LINK_GATEWAY_TO_PROCESSOR, new Fault.Latency(2_500), new BlastRadius.PercentOfRequests(10)));
        check("determinism: identical seeds give identical reports", a.render().equals(b.render()));

        // --- GameDay collects and summarises
        w = World.create();
        GameDay day = new GameDay(w.runner());
        day.run(exp(SimNetwork.LINK_GATEWAY_TO_PROCESSOR, new Fault.Latency(300), new BlastRadius.PercentOfRequests(10)));
        day.run(exp(SimNetwork.TARGET_PROCESSOR, new Fault.Error("HTTP_500"), new BlastRadius.PercentOfRequests(70)));
        check("gameday keeps every report", day.reports().size() == 2);
        check("gameday summary lists both verdicts", day.summary().contains("HYPOTHESIS HELD") && day.summary().contains("ABORTED"));

        System.out.println(passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("  FAIL: " + f);
        System.exit(failures.isEmpty() ? 0 : 1);
    }
}
