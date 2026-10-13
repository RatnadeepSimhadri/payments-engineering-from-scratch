// Tests for phases/01-complexity-and-core-structures/10-disjoint-sets. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.Random;

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
        // fresh forest: every element is its own set
        DisjointSet ds = new DisjointSet(6);
        check("n singletons", ds.componentCount() == 6 && ds.elements() == 6);
        check("find of fresh element is itself", ds.find(3) == 3);
        check("fresh elements not connected", !ds.connected(0, 1));
        check("fresh component size 1", ds.componentSize(4) == 1);
        check("fresh invariant", ds.checkInvariant());
        check("empty forest", new DisjointSet(0).componentCount() == 0 && new DisjointSet(0).checkInvariant());
        checkThrows("negative n rejected", IllegalArgumentException.class, () -> new DisjointSet(-1));
        checkThrows("out-of-range element rejected", IndexOutOfBoundsException.class, () -> ds.find(6));

        // union merges, repeated union is a no-op, connectivity is transitive
        check("first union returns true", ds.union(0, 1));
        check("union decrements components", ds.componentCount() == 5);
        check("union makes connected", ds.connected(0, 1) && ds.find(0) == ds.find(1));
        check("repeat union returns false", !ds.union(1, 0) && ds.componentCount() == 5);
        ds.union(2, 3);
        ds.union(1, 3);
        check("transitive: 0 ~ 2 via 1 and 3", ds.connected(0, 2) && ds.connected(3, 0));
        check("component size tracks merges", ds.componentSize(0) == 4 && ds.componentSize(5) == 1);
        check("unrelated element stays separate", !ds.connected(0, 5));
        check("invariant after unions", ds.checkInvariant());
        check("all representatives agree", ds.find(0) == ds.find(1) && ds.find(1) == ds.find(2) && ds.find(2) == ds.find(3));

        // union by rank: the shorter tree goes under the taller one, rank grows only on ties
        DisjointSet r = new DisjointSet(8);
        r.union(0, 1);                              // rank 1 at root
        check("tie union raises rank to 1", r.rankOf(0) == 1);
        r.union(0, 2);                              // singleton under rank-1 root: rank stays 1
        check("unequal union keeps rank", r.rankOf(0) == 1 && r.depth(2) == 1);
        r.union(3, 4);
        r.union(0, 3);                              // two rank-1 trees: rank 2
        check("tie of rank-1 trees gives rank 2", r.rankOf(0) == 2);
        check("rank bounds depth: depth <= rank everywhere", r.maxDepth() <= r.rankOf(0));
        check("rank never exceeds log2(size)", r.rankOf(0) <= 31 - Integer.numberOfLeadingZeros(r.componentSize(0)));

        // path compression: after one find, every node on the path points at the root
        DisjointSet pc = new DisjointSet(5, false, true);  // no rank so we can build a known chain
        pc.union(1, 0); pc.union(2, 1); pc.union(3, 2); pc.union(4, 3);  // chain 0 -> 1 -> 2 -> 3 -> 4
        check("naive chain has depth n-1 before find", pc.depth(0) == 4);
        int root = pc.find(0);
        check("find returns the chain root", root == 4);
        check("path compression flattens the path", pc.depth(0) == 1 && pc.depth(1) == 1 && pc.depth(2) == 1);
        check("compression preserves membership", pc.connected(0, 4) && pc.componentSize(0) == 5 && pc.checkInvariant());

        // without either heuristic the forest still answers correctly, just slowly
        DisjointSet naive = new DisjointSet(2_000, false, false);
        for (int i = 1; i < 2_000; i++) naive.union(i, i - 1);
        check("naive forest is a chain of depth n-1", naive.maxDepth() == 1_999);
        naive.resetHops();
        naive.find(0);
        check("naive find walks the whole chain", naive.hops() == 1_999);
        check("naive still correct", naive.connected(0, 1_999) && naive.componentCount() == 1 && naive.checkInvariant());

        // with both heuristics the same workload stays shallow
        DisjointSet both = new DisjointSet(2_000);
        for (int i = 1; i < 2_000; i++) both.union(i, i - 1);
        check("rank keeps the adversarial chain shallow", both.maxDepth() <= 11);
        both.resetHops();
        for (int i = 0; i < 2_000; i++) both.find(i);
        check("near-constant pointers per find", both.hops() < 2 * 2_000);

        // randomized cross-check against a brute-force label array
        int n = 300;
        DisjointSet mine = new DisjointSet(n);
        int[] label = new int[n];
        for (int i = 0; i < n; i++) label[i] = i;
        Random rnd = new Random(99);
        boolean agree = true;
        for (int step = 0; step < 2_000 && agree; step++) {
            int a = rnd.nextInt(n), b = rnd.nextInt(n);
            boolean merged = mine.union(a, b);
            boolean wasSeparate = label[a] != label[b];
            if (merged != wasSeparate) agree = false;
            if (wasSeparate) { int from = label[b], to = label[a]; for (int i = 0; i < n; i++) if (label[i] == from) label[i] = to; }
            if (step % 97 == 0) {
                for (int i = 0; i < n && agree; i++) {
                    int j = rnd.nextInt(n);
                    if (mine.connected(i, j) != (label[i] == label[j])) agree = false;
                }
            }
        }
        java.util.Set<Integer> distinct = new java.util.HashSet<>();
        for (int l : label) distinct.add(l);
        check("randomized unions agree with brute force", agree);
        check("randomized component count matches", mine.componentCount() == distinct.size() && mine.checkInvariant());

        // entity linking
        EntityLinker linker = new EntityLinker(16);
        check("link new identifiers merges", linker.link("acct:1", "device:A"));
        check("second account on same device merges", linker.link("acct:2", "device:A"));
        check("acct:1 and acct:2 share a ring through the device", linker.sameRing("acct:1", "acct:2"));
        check("ring members sorted", linker.ring("acct:2").equals(List.of("acct:1", "acct:2", "device:A")));
        check("re-observing a link returns false", !linker.link("acct:2", "device:A"));
        linker.link("acct:3", "card:X");
        check("separate ring not connected", !linker.sameRing("acct:1", "acct:3") && linker.rings() == 2);
        check("unknown identifier is in no ring", !linker.sameRing("acct:1", "acct:999") && linker.ringSize("acct:999") == 0 && linker.ring("nobody").isEmpty());
        linker.link("card:X", "acct:1");
        check("linking the rings merges them", linker.sameRing("acct:3", "acct:2") && linker.ringSize("acct:3") == 5 && linker.rings() == 1);
        check("ringsOfAtLeast filters by size", linker.ringsOfAtLeast(5).size() == 1 && linker.ringsOfAtLeast(6).isEmpty());
        EntityLinker tiny = new EntityLinker(2);
        tiny.link("a", "b");
        checkThrows("capacity exceeded is an error", IllegalStateException.class, () -> tiny.link("a", "c"));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
