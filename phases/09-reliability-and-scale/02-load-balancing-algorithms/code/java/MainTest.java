// Tests for phases/09-reliability-and-scale/02-load-balancing-algorithms. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.HashSet;
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

    static Request req(long id, String key) { return new Request(id, key, 10.0); }

    public static void main(String[] args) {
        // Rng: identical stream across languages (first values of SplitMix64 with seed 42)
        Rng rng = new Rng(42);
        check("splitmix64 seed 42 first value", rng.nextLong() == -4767286540954276203L);
        check("splitmix64 nextDouble in [0,1)", new Rng(1).nextDouble() >= 0 && new Rng(1).nextDouble() < 1);
        check("nextInt within bound", new Rng(3).nextInt(5) >= 0 && new Rng(3).nextInt(5) < 5);

        // Round robin: cycles, and the cycle shifts when a backend is evicted
        List<Backend> abc = List.of(new Backend("a"), new Backend("b"), new Backend("c"));
        RoundRobin rr = new RoundRobin();
        StringBuilder seq = new StringBuilder();
        for (int i = 0; i < 6; i++) seq.append(rr.pick(abc, req(i, "k")).id);
        check("round robin cycles a b c a b c", seq.toString().equals("abcabc"));
        List<Backend> ac = List.of(abc.get(0), abc.get(2));
        check("round robin over the healthy subset only", ac.contains(rr.pick(ac, req(7, "k"))));
        checkThrows("round robin with no backends", IllegalStateException.class, () -> rr.pick(List.of(), req(0, "k")));

        // Smooth weighted round robin: nginx's interleaving, and proportions over a full cycle
        List<Backend> weighted = List.of(new Backend("a", 5, 1), new Backend("b", 1, 1), new Backend("c", 1, 1));
        WeightedRoundRobin wrr = new WeightedRoundRobin();
        StringBuilder wseq = new StringBuilder();
        for (int i = 0; i < 7; i++) wseq.append(wrr.pick(weighted, req(i, "k")).id);
        check("smooth WRR interleaves a a b a c a a", wseq.toString().equals("aabacaa"));
        int aCount = 0;
        for (int i = 0; i < 70; i++) if (wrr.pick(weighted, req(i, "k")).id.equals("a")) aCount++;
        check("WRR gives a exactly 5/7 of 70 picks", aCount == 50);
        checkThrows("backend rejects zero weight", IllegalArgumentException.class, () -> new Backend("x", 0, 1.0));

        // Least connections: picks the emptiest, ties to the first
        Backend l1 = new Backend("l1"), l2 = new Backend("l2"), l3 = new Backend("l3");
        l1.activeConnections = 3; l2.activeConnections = 1; l3.activeConnections = 1;
        check("least connections picks fewest (first on tie)", new LeastConnections().pick(List.of(l1, l2, l3), req(0, "k")) == l2);
        l2.activeConnections = 5;
        check("least connections follows load changes", new LeastConnections().pick(List.of(l1, l2, l3), req(0, "k")) == l3);

        // Power of two choices: never the more loaded of its two samples; single backend returned directly
        Backend p1 = new Backend("p1"), p2 = new Backend("p2");
        p1.activeConnections = 10;
        PowerOfTwoChoices p2c = new PowerOfTwoChoices(new Rng(5));
        boolean alwaysLess = true;
        for (int i = 0; i < 50; i++) if (p2c.pick(List.of(p1, p2), req(i, "k")) != p2) alwaysLess = false;
        check("P2C with two backends always takes the less loaded", alwaysLess);
        check("P2C with a single backend returns it", p2c.pick(List.of(p1), req(0, "k")) == p1);

        // Balls into bins: two choices beat one, deterministically under a seed
        int one = Main.ballsInBins(10_000, 10_000, 1, new Rng(7));
        int two = Main.ballsInBins(10_000, 10_000, 2, new Rng(7));
        check("one choice max load is 6 under seed 7", one == 6);
        check("two choices max load is 3 under seed 7", two == 3);
        check("two choices strictly better than one", two < one);

        // Consistent hashing: affinity, minimal disruption, spread
        ConsistentHash ch = new ConsistentHash(100);
        List<Backend> six = Main.fleet();
        check("same key -> same backend", ch.pick(six, req(1, "merchant-77")) == ch.pick(six, req(2, "merchant-77")));
        List<Backend> five = new ArrayList<>(six);
        Backend removed = five.remove(2);
        int moved = 0, movedFromOther = 0;
        for (int k = 0; k < 1000; k++) {
            Backend before = ch.pick(six, req(k, "merchant-" + k));
            Backend after = ch.pick(five, req(k, "merchant-" + k));
            if (before != after) { moved++; if (before != removed) movedFromOther++; }
        }
        check("only keys owned by the removed backend move", movedFromOther == 0);
        check("about 1/6 of keys move (got " + moved + ")", moved > 100 && moved < 250);
        Set<String> owners = new HashSet<>();
        for (int k = 0; k < 1000; k++) owners.add(ch.pick(six, req(k, "merchant-" + k)).id);
        check("every backend owns some keys", owners.size() == 6);
        check("hash64 is stable: merchant-1", ConsistentHash.hash64("merchant-1") == ConsistentHash.hash64("merchant-1"));
        check("hash64 separates neighbours", ConsistentHash.hash64("merchant-1") != ConsistentHash.hash64("merchant-2"));
        checkThrows("ring rejects zero virtual nodes", IllegalArgumentException.class, () -> new ConsistentHash(0));

        // Health checker: consecutive failures evict, consecutive successes readmit, a stray failure does not
        Backend h = new Backend("h");
        HealthChecker hc = new HealthChecker(3, 2);
        hc.record(h, false); hc.record(h, false); hc.record(h, true); hc.record(h, false); hc.record(h, false);
        check("two failures then a success resets the streak", h.healthy);
        boolean changed = hc.record(h, false);
        check("third consecutive failure evicts", !h.healthy && changed);
        hc.record(h, true);
        check("one success is not enough to readmit", !h.healthy);
        check("second consecutive success readmits", hc.record(h, true) && h.healthy);

        // Pool: routes around evicted backends, returns null when none are left, tracks active connections
        List<Backend> pb = List.of(new Backend("x"), new Backend("y"));
        Pool pool = new Pool(pb, new RoundRobin(), new HealthChecker(1, 1));
        Backend first = pool.route(req(0, "k"));
        check("route increments active connections", first.activeConnections == 1 && first.totalAssigned == 1);
        pool.complete(first, false);
        check("one failure with threshold 1 evicts", !first.healthy && first.activeConnections == 0);
        check("routing avoids the evicted backend", pool.route(req(1, "k")) != first && pool.route(req(2, "k")) != first);
        Backend other = pb.get(0) == first ? pb.get(1) : pb.get(0);
        pool.complete(other, false);
        check("no healthy backends -> null (shed)", pool.route(req(3, "k")) == null);

        // Simulation: deterministic, and the load-aware strategies beat round robin on this fleet
        SimResult rrA = Main.simulate(new RoundRobin(), 42, false);
        SimResult rrB = Main.simulate(new RoundRobin(), 42, false);
        check("simulation is deterministic under a seed", rrA.p99Ms() == rrB.p99Ms() && rrA.maxQueue() == rrB.maxQueue());
        SimResult lc = Main.simulate(new LeastConnections(), 42, false);
        SimResult p2r = Main.simulate(new PowerOfTwoChoices(new Rng(11)), 42, false);
        check("least connections p99 far below round robin", lc.p99Ms() * 10 < rrA.p99Ms());
        check("P2C p99 far below round robin", p2r.p99Ms() * 10 < rrA.p99Ms());
        check("P2C max queue within 2x of least connections", p2r.maxQueue() <= 2 * lc.maxQueue());
        check("round robin assigns equally regardless of speed", rrA.maxAssigned() - rrA.minAssigned() <= 1);
        check("least connections sends less to the slow backend", lc.minAssigned() < rrA.minAssigned());
        SimResult outage = Main.simulate(new LeastConnections(), 42, true);
        check("eviction bounds failures during a 3s outage to a handful", outage.failed() > 0 && outage.failed() < 20);
        check("nothing shed while other backends are healthy", outage.shed() == 0);
        check("percentile nearest-rank", Simulation.percentile(new double[]{1, 2, 3, 4}, 50) == 2.0
                && Simulation.percentile(new double[]{1, 2, 3, 4}, 99) == 4.0);

        System.out.println(passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("  FAIL: " + f);
        System.exit(failures.isEmpty() ? 0 : 1);
    }
}
