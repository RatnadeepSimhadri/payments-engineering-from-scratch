// Tests for phases/05-distributed-foundations/03-lamport-and-vector-clocks. Run: javac -d .out *.java && java -cp .out MainTest
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

    static String logOf(SimulatedNetwork net) {
        StringBuilder sb = new StringBuilder();
        for (Event e : Main.totalOrder(net.allEvents())) sb.append(e).append('\n');
        return sb.toString();
    }

    /** happens-before (by vector clock) must imply a strictly smaller Lamport timestamp, for every pair. */
    static boolean clockCondition(List<Event> events) {
        for (Event x : events) for (Event y : events) {
            if (x.vector().happensBefore(y.vector()) && x.lamport().compareTo(y.lamport()) >= 0) return false;
        }
        return true;
    }

    public static void main(String[] args) {
        // Lamport clock rules and monotonicity
        LamportClock lc = new LamportClock();
        check("tick increments by one", lc.tick() == 1 && lc.tick() == 2);
        check("send is a tick", lc.send() == 3);
        check("receive jumps past a remote clock that is ahead", lc.receive(10) == 11);
        check("receive still advances when local clock is ahead", lc.receive(2) == 12);
        check("receive of equal time advances by one", lc.receive(12) == 13);
        LamportClock mono = new LamportClock();
        long prev = 0; boolean monotone = true;
        long[] remotes = {0, 5, 2, 9, 9, 1, 40, 3};
        for (int i = 0; i < 50; i++) {
            long now = (i % 3 == 0) ? mono.receive(remotes[i % remotes.length]) : mono.tick();
            if (now <= prev) monotone = false;
            prev = now;
        }
        check("lamport clock is strictly monotonic across 50 mixed operations", monotone && mono.time() == prev);

        // Lamport total order with tie-break
        LamportTimestamp t1 = new LamportTimestamp(3, "payments"), t2 = new LamportTimestamp(3, "refunds"), t3 = new LamportTimestamp(2, "refunds");
        check("lamport compares by time first", t3.compareTo(t1) < 0 && t1.compareTo(t3) > 0);
        check("lamport ties break by node id", t1.compareTo(t2) < 0 && t2.compareTo(t1) > 0);
        check("lamport equal only when both match", t1.compareTo(new LamportTimestamp(3, "payments")) == 0);

        // Vector clock algebra
        VectorClock zero = new VectorClock(3);
        VectorClock v100 = zero.increment(0);
        check("increment touches only its own component", v100.get(0) == 1 && v100.get(1) == 0 && v100.get(2) == 0);
        check("increment is immutable", zero.get(0) == 0);
        VectorClock a = VectorClock.of(2, 0, 1), b = VectorClock.of(1, 3, 1);
        check("merge is componentwise max", a.merge(b).equals(VectorClock.of(2, 3, 1)));
        check("merge is commutative", a.merge(b).equals(b.merge(a)));
        check("merge is idempotent", a.merge(a).equals(a) && a.merge(b).merge(b).equals(a.merge(b)));
        check("merge is associative", a.merge(b).merge(zero).equals(a.merge(b.merge(zero))));
        check("merge result is AFTER or EQUAL to both inputs", a.compare(a.merge(b)) == Relation.BEFORE && b.compare(a.merge(b)) == Relation.BEFORE);
        check("BEFORE when every component <= and one <", VectorClock.of(1, 0, 0).compare(VectorClock.of(2, 0, 0)) == Relation.BEFORE);
        check("AFTER is the mirror of BEFORE", VectorClock.of(2, 0, 0).compare(VectorClock.of(1, 0, 0)) == Relation.AFTER);
        check("EQUAL when identical", VectorClock.of(2, 2, 2).compare(VectorClock.of(2, 2, 2)) == Relation.EQUAL);
        check("CONCURRENT when each is ahead somewhere", a.compare(b) == Relation.CONCURRENT && a.concurrentWith(b) && b.concurrentWith(a));
        check("happensBefore is strict", !VectorClock.of(1, 1, 1).happensBefore(VectorClock.of(1, 1, 1)));
        checkThrows("mismatched cluster sizes rejected", IllegalArgumentException.class, () -> VectorClock.of(1, 0).compare(VectorClock.of(1, 0, 0)));

        // Node send/receive
        Node payments = new Node("payments", 0, 3), refunds = new Node("refunds", 1, 3);
        payments.localEvent("a");
        Message m = payments.send("refunds", "x");
        check("message carries sender's clocks after the send tick", m.lamportTime() == 2 && m.vector().equals(VectorClock.of(2, 0, 0)));
        refunds.localEvent("b");
        Event recv = refunds.receive(m);
        check("receiver lamport = max(1,2)+1", recv.lamport().time() == 3);
        check("receiver vector = merge then increment own", recv.vector().equals(VectorClock.of(2, 2, 0)));
        check("send event happens-before receive event", m.vector().happensBefore(recv.vector()));
        check("local event before the receive is concurrent with the send", refunds.log.get(0).vector().concurrentWith(m.vector()));

        // Simulated network: determinism and delivery
        SimulatedNetwork n1 = Main.concurrentRefundScenario(7), n2 = Main.concurrentRefundScenario(7);
        check("same seed gives identical event logs", logOf(n1).equals(logOf(n2)));
        check("all messages delivered", n1.inFlight() == 0 && n1.delivered() == 2);
        check("network tick advances past zero", n1.tick() >= 1);
        SimulatedNetwork noDelay = new SimulatedNetwork(1, 0);
        check("maxDelay 0 always yields delay 0", noDelay.nextDelay() == 0 && noDelay.nextDelay() == 0);
        SimulatedNetwork lcg = new SimulatedNetwork(7, 3);
        check("LCG schedule for seed 7 is identical in every language", lcg.nextDelay() == 2 && lcg.nextDelay() == 3 && lcg.nextDelay() == 1);
        checkThrows("negative maxDelay rejected", IllegalArgumentException.class, () -> new SimulatedNetwork(1, -1));
        checkThrows("send to unknown node rejected", IllegalArgumentException.class,
                () -> noDelay.send(new Message("payments", "mars", "p", 1, VectorClock.of(1, 0, 0))));
        check("seed 7 delivers captured first at the ledger", n1.node("ledger").log.get(0).description().startsWith("recv captured"));
        check("seed 1 delivers refunded first at the ledger", Main.concurrentRefundScenario(1).node("ledger").log.get(0).description().startsWith("recv refunded"));

        // Scenario 1: the refund is caused by the capture notice
        SimulatedNetwork causal = Main.causalRefundScenario(7);
        Event capturedC = causal.node("payments").log.get(0), refundedC = causal.node("refunds").log.get(1);
        check("causal: refunded posting is 'post refunded'", refundedC.description().startsWith("post refunded"));
        check("causal: captured happens-before refunded", capturedC.vector().happensBefore(refundedC.vector()));
        check("causal: captured lamport < refunded lamport", capturedC.lamport().compareTo(refundedC.lamport()) < 0);
        check("causal: no concurrent postings at all", Main.concurrentPairs(Main.postings(causal.allEvents())).isEmpty());
        check("causal: ledger settle is after everything", causal.allEvents().stream()
                .filter(e -> !e.description().startsWith("post settle"))
                .allMatch(e -> e.vector().happensBefore(causal.node("ledger").log.get(2).vector())));

        // Scenario 2: concurrent updates are detected, causal ones are not
        List<Event> events = n1.allEvents();
        Event captured = n1.node("payments").log.get(0), refunded = n1.node("refunds").log.get(0);
        Event settle = n1.node("ledger").log.get(n1.node("ledger").log.size() - 1);
        check("concurrent: captured and refunded are concurrent", captured.vector().concurrentWith(refunded.vector()));
        check("concurrent: lamport still orders them (captured first by tie-break)", captured.lamport().compareTo(refunded.lamport()) < 0 && captured.lamport().time() == refunded.lamport().time());
        check("concurrent: settle happens after both", captured.vector().happensBefore(settle.vector()) && refunded.vector().happensBefore(settle.vector()));
        check("postings() keeps only ledger postings", Main.postings(events).size() == 3);
        check("exactly one concurrent pair among postings", Main.concurrentPairs(Main.postings(events)).size() == 1);
        check("the only concurrent pairs are between services", Main.concurrentPairs(events).stream().allMatch(p -> !p[0].nodeId().equals(p[1].nodeId())));

        // The clock condition: happens-before implies smaller Lamport time; the converse fails
        check("happens-before implies lamport <", clockCondition(events));
        List<Event> order = Main.totalOrder(events);
        boolean consistent = true;
        for (Event x : events) for (Event y : events) {
            if (x.vector().happensBefore(y.vector()) && order.indexOf(x) > order.indexOf(y)) consistent = false;
        }
        check("total order respects causality", consistent);
        boolean converseFails = false;
        for (int i = 0; i < order.size(); i++) for (int j = i + 1; j < order.size(); j++) {
            if (order.get(i).vector().concurrentWith(order.get(j).vector())) converseFails = true;
        }
        check("some lamport-ordered pair is actually concurrent", converseFails);
        check("total order has no duplicates and keeps every event", order.size() == events.size() && order.stream().distinct().count() == events.size());

        // Different seeds reorder deliveries but never break the clock condition
        boolean allSeedsHold = true;
        for (long seed = 1; seed <= 30; seed++) {
            if (!clockCondition(Main.concurrentRefundScenario(seed).allEvents())) allSeedsHold = false;
            if (!clockCondition(Main.causalRefundScenario(seed).allEvents())) allSeedsHold = false;
        }
        check("clock condition holds across 30 seeds in both scenarios", allSeedsHold);

        // Money is integer minor units
        check("money toString", Money.ofMinor(4_999, Currency.USD).toString().equals("49.99 USD"));
        check("money negative toString", Money.ofMinor(-4_999, Currency.USD).toString().equals("-49.99 USD"));
        check("capture plus refund nets to zero", Main.CAPTURE.plus(Main.REFUND).minorUnits() == 0);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
