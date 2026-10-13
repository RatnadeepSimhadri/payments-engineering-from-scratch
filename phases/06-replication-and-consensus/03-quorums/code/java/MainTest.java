// Tests for phases/06-replication-and-consensus/03-quorums. Run: javac -d .out *.java && java -cp .out MainTest
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

    static final QuorumConfig STRICT_222 = new QuorumConfig(3, 2, 2, false, false);

    public static void main(String[] args) {
        String key = "merchant:m-77:settlement-currency";

        // --- configuration validation and the overlap predicate
        check("R + W > N overlaps", STRICT_222.overlaps());
        check("R + W = N does not overlap", !new QuorumConfig(3, 1, 2, false, false).overlaps());
        checkThrows("R > N is rejected", IllegalArgumentException.class, () -> new QuorumConfig(3, 4, 1, false, false));
        checkThrows("W = 0 is rejected", IllegalArgumentException.class, () -> new QuorumConfig(3, 1, 0, false, false));
        checkThrows("fewer nodes than N is rejected", IllegalArgumentException.class, () -> new QuorumStore(2, STRICT_222, 1, 0.0));

        // --- versions: LWW with a deterministic tie-break
        check("later timestamp is newer", new Versioned("b", 2, 0).newerThan(new Versioned("a", 1, 9)));
        check("equal timestamp: higher writer id wins", new Versioned("b", 2, 3).newerThan(new Versioned("a", 2, 1)));
        check("a version is not newer than itself", !new Versioned("a", 2, 1).newerThan(new Versioned("a", 2, 1)));
        check("anything is newer than NONE", new Versioned("a", 0, 0).newerThan(Versioned.NONE));

        // --- placement is deterministic and the ring is N home nodes then the rest
        QuorumStore s = new QuorumStore(5, STRICT_222, 1, 0.0);
        check("home set has N nodes", s.homeNodes(key).size() == 3);
        check("fallback ring holds the other nodes", s.fallbackNodes(key).size() == 2 && !s.fallbackNodes(key).contains(s.homeNodes(key).get(0)));
        check("placement is a pure function of the key", s.homeNodes(key).equals(new QuorumStore(5, STRICT_222, 99, 0.5).homeNodes(key)));

        // --- happy path: a strict quorum write and read
        List<Integer> home = s.homeNodes(key);
        check("write with all replicas up gets N acks", s.put(home.get(0), key, "EUR") == 3);
        ReadResult r = s.get(home.get(2), key);
        check("read returns the written value", "EUR".equals(r.value()) && r.responses() == 2);
        check("coordinator consults its own copy first", r.respondents().get(0) == home.get(2));
        check("replicasHolding counts the home set", s.replicasHolding(key, "EUR") == 3);

        // --- the guarantee: with R + W > N and strict quorums, a read after a write never misses it, even with a node down
        s.network.crash(home.get(1));
        s.put(home.get(0), key, "GBP");
        s.network.recover(home.get(1));
        s.network.crash(home.get(0));
        ReadResult overlapping = s.get(home.get(1), key);
        check("strict R=2 read overlaps the W=2 write set and sees the new value", "GBP".equals(overlapping.value()));
        check("the stale replica was read but outvoted by the newer version", overlapping.responses() == 2);

        // --- failure is reported, never faked
        s.network.crash(home.get(2));   // now two of three home nodes are down
        checkThrows("write with fewer than W reachable replicas throws", QuorumNotReached.class, () -> s.put(home.get(1), key, "CHF"));
        checkThrows("read with fewer than R reachable replicas throws", QuorumNotReached.class, () -> s.get(home.get(1), key));
        check("the failed write still landed on the reachable replica (no rollback)", "CHF".equals(s.nodes.get(home.get(1)).data.get(key).value()));
        s.network.recover(home.get(0)); s.network.recover(home.get(2));

        // --- R + W <= N: the quorums can miss each other
        QuorumStore weak = new QuorumStore(5, new QuorumConfig(3, 1, 1, false, false), 1, 0.0);
        weak.put(home.get(0), key, "EUR");
        weak.network.crash(home.get(2));
        weak.put(home.get(1), key, "GBP");
        weak.network.recover(home.get(2));
        check("R=1 read from the briefly-unreachable node returns the old value", "EUR".equals(weak.get(home.get(2), key).value()));
        check("R=1 read from a node that saw the write returns the new value", "GBP".equals(weak.get(home.get(0), key).value()));

        // --- sloppy quorums: available, and stale even though R + W > N
        QuorumStore sloppy = new QuorumStore(5, new QuorumConfig(3, 2, 2, true, false), 1, 0.0);
        sloppy.network.crash(home.get(1)); sloppy.network.crash(home.get(2));
        int acks = sloppy.put(home.get(0), key, "EUR");
        check("sloppy write succeeds with two home nodes down by spilling onto the ring", acks >= 2 && sloppy.hintedWrites == 2);
        check("the spill nodes hold hints for the absent home nodes", sloppy.nodes.get(sloppy.fallbackNodes(key).get(0)).hintCount() == 1);
        check("only one home replica actually holds the value", sloppy.replicasHolding(key, "EUR") == 1);
        QuorumStore strictSame = new QuorumStore(5, STRICT_222, 1, 0.0);
        strictSame.network.crash(home.get(1)); strictSame.network.crash(home.get(2));
        checkThrows("the same write fails under a strict quorum", QuorumNotReached.class, () -> strictSame.put(home.get(0), key, "EUR"));
        sloppy.network.recover(home.get(1)); sloppy.network.recover(home.get(2)); sloppy.network.crash(home.get(0));
        ReadResult stale = sloppy.get(home.get(1), key);
        check("sloppy read with R=2 misses the value before handoff: R + W > N is not enough", stale.value() == null);
        int delivered = sloppy.handoff();
        check("hinted handoff delivers one value per absent home node", delivered == 2);
        check("after handoff the same read sees the value", "EUR".equals(sloppy.get(home.get(1), key).value()));
        check("hints are cleared once delivered", sloppy.nodes.get(sloppy.fallbackNodes(key).get(0)).hintCount() == 0);
        check("handoff is idempotent", sloppy.handoff() == 0);

        // --- read repair
        QuorumStore rr = new QuorumStore(5, new QuorumConfig(3, 2, 2, false, true), 1, 0.0);
        rr.network.crash(home.get(2));
        rr.put(home.get(0), key, "EUR");
        rr.network.recover(home.get(2));
        check("replica that was down is stale before any read", rr.replicasHolding(key, "EUR") == 2);
        rr.network.crash(home.get(0));
        ReadResult repaired = rr.get(home.get(1), key);
        check("read repair fixes the stale responder", repaired.repaired() == 1 && rr.replicasHolding(key, "EUR") == 3);
        check("a second read repairs nothing", rr.get(home.get(1), key).repaired() == 0);
        check("read repair never writes an older version over a newer one", !rr.nodes.get(home.get(1)).put(key, new Versioned("OLD", 0, 0)));

        // --- LWW loses a concurrent update silently
        QuorumStore lww = new QuorumStore(5, STRICT_222, 1, 0.0);
        String bal = "wallet:w-9:balance";
        lww.put(0, bal, "100.00 USD");
        lww.put(1, bal, "70.00 USD");
        lww.put(3, bal, "150.00 USD");
        check("both concurrent writes succeeded and the later timestamp won", "150.00 USD".equals(lww.get(2, bal).value()));
        check("the debit is gone from every replica", lww.replicasHolding(bal, "70.00 USD") == 0);

        // --- anti-entropy converges without reads
        QuorumStore ae = new QuorumStore(5, new QuorumConfig(3, 2, 2, true, false), 1, 0.0);
        ae.network.crash(2);
        for (int i = 0; i < 8; i++) ae.put(i % 5 == 2 ? 0 : i % 5, "cfg:" + i, "value" + i);
        ae.network.recover(2);
        int disagreeBefore = 0;
        for (int i = 0; i < 8; i++) if (!ae.replicasAgree("cfg:" + i)) disagreeBefore++;
        ae.antiEntropy();
        int disagreeAfter = 0;
        for (int i = 0; i < 8; i++) if (!ae.replicasAgree("cfg:" + i)) disagreeAfter++;
        check("some keys disagree after a node missed writes", disagreeBefore > 0);
        check("anti-entropy makes every key's replicas agree", disagreeAfter == 0);
        check("anti-entropy is idempotent", ae.antiEntropy() == 0);

        // --- the property across seeds: strict overlapping quorums never return a stale value; the others do
        int strictStale = 0, weakStale = 0, sloppyStale = 0;
        for (long seed = 1; seed <= 12; seed++) {
            strictStale += Experiment.run(STRICT_222, 5, seed, 0.05, 300, 20).staleReads();
            strictStale += Experiment.run(new QuorumConfig(3, 3, 1, false, false), 5, seed, 0.05, 300, 20).staleReads();
            weakStale += Experiment.run(new QuorumConfig(3, 1, 1, false, false), 5, seed, 0.05, 300, 20).staleReads();
            sloppyStale += Experiment.run(new QuorumConfig(3, 2, 2, true, false), 5, seed, 0.05, 300, 20).staleReads();
        }
        check("strict R + W > N: zero stale reads across 12 seeds with drops and crashes", strictStale == 0);
        check("R + W <= N: stale reads occur", weakStale > 0);
        check("sloppy R + W > N: stale reads still occur", sloppyStale > 0);

        // --- determinism
        Experiment.Outcome a = Experiment.run(new QuorumConfig(3, 2, 2, true, true), 5, 42, 0.1, 200, 15);
        Experiment.Outcome b = Experiment.run(new QuorumConfig(3, 2, 2, true, true), 5, 42, 0.1, 200, 15);
        check("same seed, same outcome", a.equals(b));

        // --- partitions are a network condition, not a node condition
        QuorumStore part = new QuorumStore(5, STRICT_222, 1, 0.0);
        part.network.partition(Set.of(home.get(0)));
        checkThrows("a coordinator alone on its side cannot reach W", QuorumNotReached.class, () -> part.put(home.get(0), key, "EUR"));
        check("the majority side still writes", part.put(home.get(1), key, "EUR") == 2);
        part.network.heal();
        check("after heal the isolated coordinator reaches a read quorum again", part.get(home.get(0), key).responses() == 2);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
