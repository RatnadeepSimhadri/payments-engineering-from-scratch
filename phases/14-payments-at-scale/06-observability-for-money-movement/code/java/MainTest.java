// Tests for phases/14-payments-at-scale/06-observability-for-money-movement. Run: javac -d .out *.java && java -cp .out MainTest
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.TreeSet;

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

    static final Currency USD = Currency.USD;

    static Money usd(long minor) { return Money.ofMinor(minor, USD); }

    static boolean has(List<Alert> alerts, String name) {
        return alerts.stream().anyMatch(a -> a.name().equals(name));
    }

    static Alert get(List<Alert> alerts, String name) {
        return alerts.stream().filter(a -> a.name().equals(name)).findFirst().orElse(null);
    }

    public static void main(String[] args) {
        // burn-rate arithmetic, exact
        Slo slo = new Slo("x", 1_000);
        check("burn 6/200 at 0.1% budget is 30.00", slo.burnRateX100(6, 200) == 3000);
        check("burn 40/2400 is 16.66 (rounded down)", slo.burnRateX100(40, 2_400) == 1666);
        check("burn exactly at budget is 1.00", slo.burnRateX100(1, 1_000) == 100);
        check("burn with no traffic is 0", slo.burnRateX100(0, 0) == 0);
        check("bps rounds down", Ratio.bps(1, 3) == 3333 && Ratio.bps(0, 0) == -1);

        // stuck detection by state age, with an injected clock
        ManualClock clock = new ManualClock(Instant.parse("2026-03-31T12:00:00Z"));
        MoneyMovementMonitor m = new MoneyMovementMonitor(clock, USD);
        m.transition("p1", "proc_a", usd(40_000), PaymentState.CAPTURE_PENDING);
        m.transition("p2", "proc_a", usd(5_000), PaymentState.AUTHORIZED);
        clock.advance(Duration.ofMinutes(15));
        check("exactly at the limit is not stuck", m.stuck().isEmpty());
        clock.advance(Duration.ofMinutes(1));
        List<StuckPayment> stuck = m.stuck();
        check("capture pending past 15m is stuck", stuck.size() == 1 && stuck.get(0).paymentId().equals("p1"));
        check("authorized for 16m is not stuck", stuck.stream().noneMatch(s -> s.paymentId().equals("p2")));
        check("stuck age is measured from entering the state", stuck.get(0).age().equals(Duration.ofMinutes(16)));
        Alert stuckAlert = get(m.evaluateAlerts(), "stuck-payments");
        check("400.00 stuck is a ticket (page threshold 1000.00)", stuckAlert != null && stuckAlert.severity() == Severity.TICKET);
        m.transition("p3", "proc_b", usd(70_000), PaymentState.CAPTURE_PENDING);
        clock.advance(Duration.ofMinutes(16));
        Alert paged = get(m.evaluateAlerts(), "stuck-payments");
        check("1100.00 stuck pages", paged != null && paged.severity() == Severity.PAGE);
        check("oldest stuck listed first", m.stuck().get(0).paymentId().equals("p1"));
        m.transition("p1", "proc_a", usd(40_000), PaymentState.CAPTURED);
        check("a transition resets the age", m.ageOf("p1").isZero() && m.stuck().size() == 1);
        m.transition("p1", "proc_a", usd(40_000), PaymentState.SETTLED);
        check("terminal state stops tracking", m.ageOf("p1") == null);

        // in-flight exposure per state
        Map<PaymentState, Money> exposure = m.inFlightExposure();
        check("exposure by state", exposure.get(PaymentState.AUTHORIZED).equals(usd(5_000))
                && exposure.get(PaymentState.CAPTURE_PENDING).equals(usd(70_000)));
        check("total in flight", m.totalInFlight().equals(usd(75_000)));
        check("no terminal states in exposure", !exposure.containsKey(PaymentState.SETTLED) && !exposure.containsKey(PaymentState.FAILED));
        checkThrows("foreign currency rejected", CurrencyMismatchException.class,
                () -> m.transition("e", "proc_a", Money.ofMinor(1, Currency.EUR), PaymentState.CREATED));
        long[] hist = m.stateAgeHistogram().get(PaymentState.CAPTURE_PENDING);
        check("histogram puts a 16m-old payment in the 15-60m band", hist[3] == 1);

        // auth rate by processor, windowed; declines are not errors
        ManualClock c2 = new ManualClock(Instant.parse("2026-03-31T12:00:00Z"));
        MoneyMovementMonitor a = new MoneyMovementMonitor(c2, USD);
        for (int minute = 0; minute < 60; minute++) {
            for (int i = 0; i < 10; i++) {
                a.authResult("proc_a", i == 0 ? AuthOutcome.ISSUER_DECLINE : AuthOutcome.APPROVED);
                a.authResult("proc_b", minute >= 50 && i < 5 ? AuthOutcome.PROCESSOR_ERROR : i == 0 ? AuthOutcome.ISSUER_DECLINE : AuthOutcome.APPROVED);
            }
            if (minute < 59) c2.advance(Duration.ofMinutes(1));
        }
        check("proc_a approves 90.00%", a.authRateBps("proc_a", Duration.ofHours(1)) == 9000);
        check("proc_b 15m rate: (45 + 50) / 150 = 63.33%", a.authRateBps("proc_b", Duration.ofMinutes(15)) == 6333);
        check("50 attempts in 5m is below the 100 minimum: no rate", a.attempts("proc_a", Duration.ofMinutes(5)) == 50
                && a.authRateBps("proc_a", Duration.ofMinutes(5)) == -1);
        check("issuer declines do not burn the SLO", a.burnRateX100(Duration.ofMinutes(5)) == 25_000);
        List<Alert> alerts = a.evaluateAlerts();
        check("processor-specific drop pages", has(alerts, "auth-rate-drop:proc_b") && !has(alerts, "auth-rate-drop:proc_a"));
        check("1h page rule fires: 50 errors / 1200 = burn 41.66 and 5m burn 250", has(alerts, "slo-burn-60m"));
        check("6h rule not armed with 1h of history", !has(alerts, "slo-burn-360m"));

        // multiwindow: a spike that has ended does not page
        ManualClock c3 = new ManualClock(Instant.parse("2026-03-31T12:00:00Z"));
        MoneyMovementMonitor s = new MoneyMovementMonitor(c3, USD);
        for (int minute = 0; minute < 60; minute++) {
            for (int i = 0; i < 40; i++) s.authResult("proc_a", minute >= 40 && minute < 50 && i < 4 ? AuthOutcome.PROCESSOR_ERROR : AuthOutcome.APPROVED);
            if (minute < 59) c3.advance(Duration.ofMinutes(1));
        }
        check("long window still hot after the spike", s.burnRateX100(Duration.ofHours(1)) >= 1440);
        check("short window has recovered", s.burnRateX100(Duration.ofMinutes(5)) == 0);
        check("so the page rule stays quiet", !has(s.evaluateAlerts(), "slo-burn-60m"));

        // reconciliation break rate
        MoneyMovementMonitor r = new MoneyMovementMonitor(clock, USD);
        r.reconRun(9_990, 10);
        check("10 breaks in 10,000 is 0.10%", r.reconBreakRateBps(4) == 10 && !has(r.evaluateAlerts(), "recon-break-rate"));
        r.reconRun(9_900, 100);
        check("110 breaks in 20,000 is 0.55% and tickets", r.reconBreakRateBps(4) == 55 && has(r.evaluateAlerts(), "recon-break-rate"));

        // the seeded simulation: alerts fire after the faults, never before, and in a sensible order
        Simulation sim = new Simulation(42);
        sim.run(Simulation.MINUTES, new TreeSet<>(List.of(430)));
        Integer stuckAt = sim.firstFired.get("stuck-payments");
        Integer dropAt = sim.firstFired.get("auth-rate-drop:proc_b");
        Integer fastAt = sim.firstFired.get("slo-burn-60m");
        Integer slowAt = sim.firstFired.get("slo-burn-360m");
        check("stuck captures detected 16 minutes after the webhook was lost", stuckAt != null && stuckAt == Simulation.LOST_WEBHOOK_MINUTE + 16);
        check("auth drop pages within 10 minutes of the outage", dropAt != null && dropAt > Simulation.OUTAGE_MINUTE && dropAt <= Simulation.OUTAGE_MINUTE + 10);
        check("fast burn pages within 10 minutes", fastAt != null && fastAt > Simulation.OUTAGE_MINUTE && fastAt <= Simulation.OUTAGE_MINUTE + 10);
        check("slow burn fires after fast burn", slowAt != null && slowAt > fastAt);
        check("no alert fires in the quiet morning", sim.firstFired.values().stream().allMatch(x -> x >= Simulation.LOST_WEBHOOK_MINUTE));
        check("dashboard renders the outage", sim.snapshots.get(430).contains("ALERT PAGE slo-burn-60m"));
        Simulation again = new Simulation(42);
        again.run(Simulation.MINUTES, new TreeSet<>(List.of(430)));
        check("same seed, same dashboard", again.snapshots.get(430).equals(sim.snapshots.get(430)));

        checkThrows("clock does not go backwards", IllegalArgumentException.class, () -> clock.advance(Duration.ofMinutes(-1)));

        System.out.println(passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("FAIL: " + f);
        if (!failures.isEmpty()) System.exit(1);
    }
}
