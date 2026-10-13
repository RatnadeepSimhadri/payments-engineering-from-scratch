// Tests for phases/05-distributed-foundations/13-gossip-and-failure-detection. Run: javac -d .out *.java && java -cp .out MainTest
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

    public static void main(String[] args) {
        // ---- precedence rules (SWIM section 4.2)
        Update a0 = new Update(1, MemberState.ALIVE, 0), s0 = new Update(1, MemberState.SUSPECT, 0);
        Update a1 = new Update(1, MemberState.ALIVE, 1), s1 = new Update(1, MemberState.SUSPECT, 1), d0 = new Update(1, MemberState.DEAD, 0);
        check("anything overrides unknown", a0.overrides(null));
        check("suspect(i) overrides alive(i)", s0.overrides(a0));
        check("alive(i) does not override suspect(i)", !a0.overrides(s0));
        check("alive(i+1) overrides suspect(i)", a1.overrides(s0));
        check("suspect(i+1) overrides alive(i+1)", s1.overrides(a1));
        check("suspect(i) does not override suspect(i)", !s0.overrides(s0));
        check("alive(i) does not override alive(i)", !a0.overrides(a0));
        check("dead overrides alive of any incarnation", d0.overrides(a1));
        check("nothing overrides dead", !a1.overrides(d0) && !s1.overrides(d0));

        // ---- config sanity
        checkThrows("ack timeout must fit twice in the period", IllegalArgumentException.class, () -> new Config(10, 5, 3, 3, 6, 3));

        // ---- a quiet cluster stays all-alive with no false positives
        Cluster quiet = new Cluster(6, 1, 0.0, 2);
        quiet.run(500);
        check("no suspicions in a healthy cluster", quiet.falsePositives() == 0 && quiet.nodes.stream().allMatch(n -> n.suspicionsRaised == 0));
        check("every node sees every other node alive", quiet.nodes.stream().allMatch(n -> n.aliveMembers().size() == 6));
        check("direct acks flow", quiet.node(0).directAcks > 0);

        // ---- a crashed node is suspected, then declared dead, and the news reaches everyone
        Cluster c = new Cluster(8, 3, 0.0, 2);
        c.run(100);
        c.crash(5);
        int suspectAt = c.runUntilAllBelieve(5, MemberState.SUSPECT, 2000);
        check("crashed node becomes SUSPECT everywhere", suspectAt > 0);
        int deadAt = c.runUntilAllBelieve(5, MemberState.DEAD, 2000);
        check("then DEAD everywhere", deadAt > suspectAt);
        check("not declared dead before the suspicion timeout", deadAt - 100 >= c.config.suspicionPeriods() * c.config.protocolPeriod());
        check("declared dead within a bounded time", deadAt - 100 <= 12 * c.config.protocolPeriod());
        check("no live node was ever marked dead", c.falsePositives() == 0);
        check("views converged on the dead set", c.viewsConverged());
        check("dead node is no longer probed", c.node(0).aliveMembers().size() == 7 && !c.node(0).aliveMembers().contains(5));

        // ---- determinism: same seed, same trace
        Cluster c2 = new Cluster(8, 3, 0.0, 2);
        c2.run(100);
        c2.crash(5);
        c2.runUntilAllBelieve(5, MemberState.SUSPECT, 2000);
        c2.runUntilAllBelieve(5, MemberState.DEAD, 2000);
        check("same seed replays the same events", c2.events.equals(c.events));

        // ---- indirect probing: a cut link between two nodes is not a failure
        Cluster cut = new Cluster(5, 11, 0.0, 2);
        cut.run(50);
        cut.network.cutLink(1, 2);
        cut.run(600);
        check("with ping-req, a one-link cut yields no false positive", cut.falsePositives() == 0);
        check("n1 still believes n2 alive", cut.node(1).stateOf(2) == MemberState.ALIVE);
        check("n1 relied on indirect acks", cut.node(1).indirectAcks > 0);

        // ---- without indirect probing the cut causes a suspicion, which the target refutes by bumping its incarnation
        Cluster naive = new Cluster(5, 11, 0.0, 2, new Config(12, 4, 0, 4, 6, 3));
        naive.run(50);
        naive.network.cutLink(1, 2);
        naive.run(1000);
        check("without ping-req the cut raises suspicions", naive.node(1).suspicionsRaised > 0 || naive.node(2).suspicionsRaised > 0);
        check("the suspected node refuted at least once", naive.node(1).refutations + naive.node(2).refutations > 0);
        check("refutation raised the incarnation", naive.node(1).incarnation + naive.node(2).incarnation > 0);
        check("but a persistent cut with no indirect probes eventually becomes a false positive", naive.falsePositives() > 0);

        // ---- a stale suspicion cannot override a refutation that already arrived
        SwimNode solo = new SwimNode(0, List.of(1, 2), Config.DEFAULT, new Rng(1), 0, (t, msg) -> {});
        solo.applyUpdate(new Update(1, MemberState.ALIVE, 3), 0);
        solo.applyUpdate(new Update(1, MemberState.SUSPECT, 2), 0);
        check("old suspicion ignored after newer alive", solo.stateOf(1) == MemberState.ALIVE);
        solo.applyUpdate(new Update(1, MemberState.SUSPECT, 3), 0);
        check("suspicion at the same incarnation is accepted", solo.stateOf(1) == MemberState.SUSPECT);
        solo.applyUpdate(new Update(0, MemberState.SUSPECT, 0), 0);
        check("hearing a suspicion about myself bumps my incarnation", solo.incarnation == 1 && solo.stateOf(0) == MemberState.ALIVE);
        check("the refutation is queued for gossip", solo.gossipQueueSize() >= 1);

        // ---- dissemination: an update is piggybacked a bounded number of times, then retired
        SwimNode g = new SwimNode(0, List.of(1, 2, 3, 4, 5, 6, 7), Config.DEFAULT, new Rng(2), 0, (t, msg) -> {});
        g.applyUpdate(new Update(3, MemberState.SUSPECT, 0), 0);
        int sends = 0;
        while (g.gossipQueueSize() > 0 && sends < 100) { g.takeGossip(); sends++; }
        check("update retired after lambda*log2(n) piggybacks (3*log2(8) = 9)", sends == 9);

        // ---- convergence across seeds under message loss, never a false positive
        int converged = 0, falsePositives = 0;
        for (long seed = 1; seed <= 10; seed++) {
            Cluster r = new Cluster(12, seed, 0.10, 2);
            r.run(200);
            r.crash(7);
            if (r.runUntilAllBelieve(7, MemberState.DEAD, 3000) > 0) converged++;
            falsePositives += r.falsePositives();
        }
        check("10/10 seeds converge on the crash under 10% loss", converged == 10);
        check("zero false positives across seeds", falsePositives == 0);

        // ---- load is O(1) per node per period regardless of N
        Cluster small = new Cluster(8, 5, 0.0, 2), big = new Cluster(32, 5, 0.0, 2);
        small.run(600); big.run(600);
        double perSmall = small.totalSent() / (8.0 * 50), perBig = big.totalSent() / (32.0 * 50);
        check("messages per node per period independent of N", Math.abs(perSmall - perBig) < 0.5);

        // ---- partition: each side declares the other dead; views converge within each side
        Cluster p = new Cluster(6, 21, 0.0, 2);
        p.run(100);
        p.network.partition(Set.of(0, 1, 2));
        p.run(300);
        check("minority side sees the other side dead", p.node(0).deadMembers().equals(Set.of(3, 4, 5)));
        check("majority side sees the minority dead", p.node(4).deadMembers().equals(Set.of(0, 1, 2)));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
