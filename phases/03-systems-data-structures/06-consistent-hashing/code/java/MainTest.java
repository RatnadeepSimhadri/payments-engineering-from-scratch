// Tests for phases/03-systems-data-structures/06-consistent-hashing. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.HashSet;
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

    static final List<String> SHARDS = new ArrayList<>();
    static {
        for (char c = 'a'; c <= 'j'; c++) SHARDS.add("ledger-shard-" + c);
    }

    static HashRing ringWith(List<String> nodes, int virtualNodes) {
        HashRing ring = new HashRing(virtualNodes);
        for (String n : nodes) ring.addNode(n);
        return ring;
    }

    public static void main(String[] args) {
        // hashing and lookups agree with Python and Go
        check("hashKey(acct-000123)", Hashing.hashKey("acct-000123") == 0xd43cbef4d0200057L);
        check("pointHash(ledger-shard-a, 0)", HashRing.pointHash("ledger-shard-a", 0) == 0xfb3be259e2c64489L);
        HashRing ring = ringWith(SHARDS, 100);
        check("lookup(acct-000123) = shard-c", ring.lookup("acct-000123").equals("ledger-shard-c"));
        check("lookupN(acct-000123, 3)", ring.lookupN("acct-000123", 3).equals(List.of("ledger-shard-c", "ledger-shard-d", "ledger-shard-e")));
        HashRing small = ringWith(List.of("alpha", "beta", "gamma"), 3);
        check("small ring lookup", small.lookup("acct-000001").equals("beta"));
        check("small ring lookupN", small.lookupN("acct-000001", 3).equals(List.of("beta", "alpha", "gamma")));

        // ring structure
        HashRing three = ringWith(SHARDS.subList(0, 3), 50);
        check("point count = nodes * vnodes", three.pointCount() == 150);
        boolean sorted = true;
        for (int i = 1; i < three.pointCount(); i++) {
            if (Long.compareUnsigned(three.hashAt(i - 1), three.hashAt(i)) > 0) sorted = false;
        }
        check("points sorted in unsigned order", sorted);
        check("nodes() is sorted", three.nodes().equals(SHARDS.subList(0, 3)));
        checkThrows("add duplicate node", IllegalArgumentException.class, () -> three.addNode("ledger-shard-a"));
        checkThrows("remove missing node", IllegalArgumentException.class, () -> three.removeNode("ledger-shard-z"));
        checkThrows("zero virtual nodes", IllegalArgumentException.class, () -> new HashRing(0));
        checkThrows("empty ring lookup", IllegalStateException.class, () -> new HashRing().lookup("acct-000001"));
        checkThrows("empty ring lookupN", IllegalStateException.class, () -> new HashRing().lookupN("acct-000001", 2));
        HashRing only = ringWith(List.of("only"), 100);
        boolean allOnly = true;
        for (String k : Measure.accountIds(200)) if (!only.lookup(k).equals("only")) allOnly = false;
        check("single node owns everything", allOnly);

        // wrap-around: keys above the highest point belong to the first point
        long top = ring.hashAt(ring.pointCount() - 1);
        int wrapped = 0;
        boolean wrapOk = true;
        for (String k : Measure.accountIds(20_000)) {
            if (Long.compareUnsigned(Hashing.hashKey(k), top) > 0) {
                wrapped++;
                if (!ring.lookup(k).equals(ring.ownerAt(0))) wrapOk = false;
            }
        }
        check("some keys wrap past the end", wrapped > 0);
        check("wrapped keys belong to the first point", wrapOk);

        // monotonicity: adding a node only moves keys to it
        List<String> keys = Measure.accountIds(5_000);
        HashRing grow = ringWith(SHARDS, 100);
        Map<String, String> before = grow.assign(keys);
        grow.addNode("ledger-shard-k");
        Map<String, String> after = grow.assign(keys);
        boolean monotoneAdd = true;
        for (String k : keys) {
            if (!after.get(k).equals(before.get(k)) && !after.get(k).equals("ledger-shard-k")) monotoneAdd = false;
        }
        check("adding a node only moves keys to it", monotoneAdd);

        // monotonicity: removing a node only moves its keys
        HashRing shrink = ringWith(SHARDS, 100);
        Map<String, String> b2 = shrink.assign(keys);
        shrink.removeNode("ledger-shard-c");
        Map<String, String> a2 = shrink.assign(keys);
        boolean monotoneRemove = true;
        for (String k : keys) {
            if (b2.get(k).equals("ledger-shard-c")) {
                if (a2.get(k).equals("ledger-shard-c")) monotoneRemove = false;
            } else if (!a2.get(k).equals(b2.get(k))) {
                monotoneRemove = false;
            }
        }
        check("removing a node only moves its keys", monotoneRemove);

        // fraction moved: ring about 1/(N+1), mod-N about N/(N+1)
        List<String> many = Measure.accountIds(20_000);
        HashRing r11 = ringWith(SHARDS, 100);
        Map<String, String> rb = r11.assign(many);
        r11.addNode("ledger-shard-k");
        double movedRing = Measure.fractionMoved(rb, r11.assign(many));
        check("ring moves about 1/11 of keys", movedRing > 0.05 && movedRing < 0.14);
        List<String> eleven = new ArrayList<>(SHARDS);
        eleven.add("ledger-shard-k");
        double movedMod = Measure.fractionMoved(new ModNSharder(SHARDS).assign(many), new ModNSharder(eleven).assign(many));
        check("mod-N moves almost everything", movedMod > 0.85);
        check("identical assignments move nothing", Measure.fractionMoved(rb, rb) == 0.0);
        check("empty assignments move nothing", Measure.fractionMoved(Map.of(), Map.of()) == 0.0);

        // balance improves with virtual nodes
        double[] one = Measure.imbalance(ringWith(SHARDS, 1).assign(many), SHARDS);
        double[] hundred = Measure.imbalance(ringWith(SHARDS, 100).assign(many), SHARDS);
        check("one vnode per node: busiest shard > 2x its share", one[0] > 2.0);
        check("100 vnodes per node: within 25% of ideal", hundred[0] < 1.25);
        check("coefficient of variation falls with vnodes", hundred[1] < one[1]);
        Map<String, Integer> loads = Measure.loadPerNode(ringWith(SHARDS, 100).assign(many), SHARDS);
        int total = 0;
        boolean allLoaded = true;
        for (int c : loads.values()) { total += c; if (c == 0) allLoaded = false; }
        check("every key is assigned exactly once", total == 20_000);
        check("every node gets some load", allLoaded);

        // replication preference lists
        boolean prefsOk = true;
        for (String k : Measure.accountIds(300)) {
            List<String> prefs = ring.lookupN(k, 3);
            if (prefs.size() != 3 || new HashSet<>(prefs).size() != 3 || !prefs.get(0).equals(ring.lookup(k))) prefsOk = false;
        }
        check("lookupN gives 3 distinct nodes starting with the owner", prefsOk);
        check("lookupN caps at node count", new TreeSet<>(three.lookupN("acct-000001", 8)).equals(new TreeSet<>(SHARDS.subList(0, 3))));
        checkThrows("lookupN rejects n = 0", IllegalArgumentException.class, () -> three.lookupN("acct-000001", 0));
        HashRing fail = ringWith(SHARDS, 100);
        List<String> prefs = fail.lookupN("acct-000123", 3);
        fail.removeNode(prefs.get(0));
        check("first replica takes over when the owner leaves", fail.lookup("acct-000123").equals(prefs.get(1)));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
