// Tests for phases/09-reliability-and-scale/13-graceful-degradation. Run: javac -d .out *.java && java -cp .out MainTest
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
        // --- CoDel: no drops under target ---
        CoDelQueue calm = new CoDelQueue(5, 100);
        int calmServed = 0;
        for (long now = 0; now < 1000; now++) {
            calm.enqueue(new Request(now, Priority.NORMAL, now), now);
            if (calm.dequeue(now + 2) != null) calmServed++;      // every request waits 2 ms < 5 ms target
        }
        check("calm queue serves every request", calmServed == 1000);
        check("no drops when sojourn stays under target", calm.dropped == 0);
        check("calm queue never enters dropping state", !calm.isDropping());

        // --- CoDel: drops only after a full interval above target ---
        CoDelQueue q = new CoDelQueue(5, 100);
        for (int i = 0; i < 400; i++) q.enqueue(new Request(i, Priority.NORMAL, 0), 0);
        long firstDropAt = -1;
        int served = 0;
        for (long now = 0; now < 400 && q.size() > 0; now++) {
            long before = q.dropped;
            if (q.dequeue(now) != null) served++;
            if (q.dropped > before && firstDropAt < 0) firstDropAt = now;
        }
        check("first drop happens one interval after sojourn first reached target (t=5 + 100)", firstDropAt == 105);
        check("CoDel is gentle: a handful of drops, not a purge", q.dropped >= 3 && q.dropped <= 10);
        check("served + dropped == enqueued", served + q.dropped == 400);
        check("dropping state is left when the queue empties", !q.isDropping() || q.size() == 0);
        checkThrows("target must be below interval", IllegalArgumentException.class, () -> new CoDelQueue(100, 50));

        // --- CoDel: a transient spike shorter than the interval is tolerated ---
        CoDelQueue spike = new CoDelQueue(5, 100);
        for (int i = 0; i < 50; i++) spike.enqueue(new Request(i, Priority.HIGH, 0), 0);
        long now = 0;
        while (spike.size() > 0) { spike.dequeue(now); now++; }   // drains in 50 ms, max sojourn 49 ms > target but < interval
        check("a 50 ms spike within a 100 ms interval drops nothing", spike.dropped == 0);
        check("but the sojourn was observed above target", spike.maxSojournSeen >= 40);

        // --- brownout controller: escalation, hysteresis, flags ---
        Flags flags = new Flags();
        BrownoutController bc = new BrownoutController(5, 100, 3, flags, 0);
        check("starts NORMAL with full fraud", bc.level() == BrownoutController.Level.NORMAL && flags.get("fraud.mode", "").equals("full"));
        bc.observe(50);
        bc.tick(100);
        check("one bad window escalates one level", bc.level() == BrownoutController.Level.SHED_LOW);
        check("escalation flips fraud to rules-only", flags.get("fraud.mode", "").equals("rules_only"));
        bc.observe(50); bc.tick(200);
        bc.observe(50); bc.tick(300);
        bc.observe(50); bc.tick(400);
        check("escalates at most one level per window, capped at CRITICAL_ONLY", bc.level() == BrownoutController.Level.CRITICAL_ONLY);
        check("CRITICAL_ONLY sheds HIGH, NORMAL, LOW", bc.level().sheds(Priority.HIGH) && bc.level().sheds(Priority.LOW) && !bc.level().sheds(Priority.CRITICAL));
        bc.observe(1); bc.tick(500);
        bc.observe(1); bc.tick(600);
        check("two calm windows are not enough to de-escalate with cooldown 3", bc.level() == BrownoutController.Level.CRITICAL_ONLY);
        bc.observe(1); bc.tick(700);
        check("third calm window de-escalates one level", bc.level() == BrownoutController.Level.SHED_NORMAL);
        bc.observe(4); bc.tick(800);
        bc.observe(4); bc.tick(900);
        bc.observe(4); bc.tick(1000);
        check("sojourn in the hysteresis band (target/2, target] holds the level", bc.level() == BrownoutController.Level.SHED_NORMAL);
        check("transitions are recorded", bc.transitions.size() == 4);
        check("time at level is accounted", bc.millisAtLevel[BrownoutController.Level.NORMAL.ordinal()] == 100);
        check("SHED_LOW sheds only LOW", BrownoutController.Level.SHED_LOW.sheds(Priority.LOW) && !BrownoutController.Level.SHED_LOW.sheds(Priority.NORMAL));
        check("NORMAL sheds nothing", !BrownoutController.Level.NORMAL.sheds(Priority.LOW));

        // --- fraud scorer follows the flag ---
        FraudScorer fs = new FraudScorer();
        Flags f2 = new Flags();
        check("full model costs more than rules", fs.serviceMillis(f2) == FraudScorer.FULL_MILLIS);
        f2.set("fraud.mode", "rules_only");
        check("rules-only is cheap", fs.serviceMillis(f2) == FraudScorer.RULES_ONLY_MILLIS);

        // --- policy admission ---
        DegradingPolicy pol = new DegradingPolicy(5, 100, 100, 3, 0);
        Outcome o = new Outcome();
        check("NORMAL level admits LOW", pol.admit(new Request(1, Priority.LOW, 0), 0, o));
        check("priority order: CRITICAL served before LOW", pol.admit(new Request(2, Priority.CRITICAL, 0), 0, o) && pol.next(0, o).priority == Priority.CRITICAL);
        check("then LOW", pol.next(0, o).priority == Priority.LOW);
        check("empty policy returns null", pol.next(0, o) == null);

        // --- the measured claim: under overload, shedding protects CRITICAL ---
        Simulation sim = Main.standardScenario(11);
        Outcome fifo = sim.run(new FifoPolicy());
        DegradingPolicy deg = new DegradingPolicy(Main.TARGET, Main.INTERVAL, Main.WINDOW, Main.COOLDOWN, 0);
        Outcome shed = sim.run(deg);
        check("same offered load for both policies (seeded)", fifo.totalOffered() == shed.totalOffered());
        check("FIFO CRITICAL p99 is in the seconds", fifo.percentile(Priority.CRITICAL, 0.99) > 1000);
        check("shedding keeps CRITICAL p99 under 100 ms", shed.percentile(Priority.CRITICAL, 0.99) < 100);
        check("shedding keeps CRITICAL p50 near service time", shed.percentile(Priority.CRITICAL, 0.50) <= 10);
        check("shedding completes nearly every CRITICAL request", shed.completed(Priority.CRITICAL) >= shed.offered.get(Priority.CRITICAL) - 10);
        check("CRITICAL is never shed at the door", shed.shedAtDoor.get(Priority.CRITICAL) == 0);
        check("LOW is shed the most", shed.shedAtDoor.get(Priority.LOW) > shed.shedAtDoor.get(Priority.NORMAL) * 0.9 && shed.shedAtDoor.get(Priority.LOW) > 1000);
        check("FIFO sheds nothing and leaves a backlog", fifo.totalShed() == 0 && fifo.stillQueued > 1000);
        check("shedding leaves no meaningful backlog", shed.stillQueued < 50);
        check("conservation: offered == completed + shed + dropped + queued (shedding)",
                shed.totalOffered() == shed.totalCompleted() + shed.totalShed() + shed.totalDropped() + shed.stillQueued);
        check("conservation: offered == completed + queued (fifo)", fifo.totalOffered() == fifo.totalCompleted() + fifo.stillQueued);
        check("brownout escalated during overload and recovered after", !deg.brownout.transitions.isEmpty()
                && deg.brownout.level() == BrownoutController.Level.NORMAL && deg.flags.get("fraud.mode", "").equals("full"));
        check("never reached CRITICAL_ONLY in this scenario", deg.brownout.millisAtLevel[BrownoutController.Level.CRITICAL_ONLY.ordinal()] == 0);

        // --- light load: the degrading policy is invisible ---
        Simulation light = new Simulation(8, 3, 600, 600, 0, 0, 3000);
        DegradingPolicy quiet = new DegradingPolicy(Main.TARGET, Main.INTERVAL, Main.WINDOW, Main.COOLDOWN, 0);
        Outcome calmOut = light.run(quiet);
        check("no shedding or drops under normal load", calmOut.totalShed() == 0 && calmOut.totalDropped() == 0);
        check("stays NORMAL under normal load", quiet.brownout.transitions.isEmpty());

        // --- determinism ---
        Outcome again = Main.standardScenario(11).run(new FifoPolicy());
        check("simulation is deterministic for a seed", again.totalCompleted() == fifo.totalCompleted() && again.percentile(Priority.LOW, 0.99) == fifo.percentile(Priority.LOW, 0.99));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
