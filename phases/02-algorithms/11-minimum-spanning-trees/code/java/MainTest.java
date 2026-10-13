// Tests for phases/02-algorithms/11-minimum-spanning-trees. Run: javac -d .out *.java && java -cp .out MainTest
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
        WeightedGraph g = Main.siteNetwork();

        // union-find
        UnionFind uf = new UnionFind(List.of("a", "b", "c", "d"));
        check("fresh sets are disjoint", uf.components() == 4 && !uf.connected("a", "b"));
        check("union merges", uf.union("a", "b") && uf.connected("a", "b") && uf.components() == 3);
        check("union of same set returns false", !uf.union("b", "a") && uf.components() == 3);
        check("transitive", uf.union("c", "d") && uf.union("b", "c") && uf.connected("a", "d") && uf.components() == 1);
        check("find is a fixed point", uf.find(uf.find("d")).equals(uf.find("d")));

        // edge validation
        checkThrows("self-loop rejected", IllegalArgumentException.class, () -> new WeightedEdge("a", "a", 1));
        checkThrows("negative weight rejected", IllegalArgumentException.class, () -> new WeightedEdge("a", "b", -1));
        check("other endpoint", new WeightedEdge("a", "b", 1).other("a").equals("b"));

        // Kruskal on the site network: the hand trace
        SpanningForest k = Mst.kruskal(g);
        check("kruskal is a tree", k.isTree() && k.edges().size() == 7);
        check("kruskal total 39", k.totalWeight() == 39);
        check("kruskal order is lightest-first with cycle skips", weights(k).equals(List.of(2L, 3L, 4L, 5L, 6L, 7L, 12L)));
        check("kruskal skips newyork-chicago 9", k.edges().stream().noneMatch(e -> e.sameEndpoints(new WeightedEdge("newyork", "chicago", 0))));
        check("kruskal passes the independent checker", Mst.isMinimumSpanningForest(g, k));

        // Prim agrees
        SpanningForest p = Mst.prim(g, "ashburn");
        check("prim total 39", p.totalWeight() == 39 && p.isTree());
        check("prim grows from the start node", p.edges().get(0).sameEndpoints(new WeightedEdge("ashburn", "newyork", 4)));
        check("prim and kruskal choose the same edges (distinct weights)", Main.sameEdgeSet(k, p));
        check("prim from any start gives the same total", g.nodes().stream().allMatch(n -> Mst.prim(g, n).totalWeight() == 39));
        checkThrows("prim rejects unknown start", IllegalArgumentException.class, () -> Mst.prim(g, "mars"));

        // the checker rejects wrong answers
        List<WeightedEdge> wrong = new ArrayList<>(k.edges());
        wrong.set(wrong.size() - 1, g.edges().get(5));   // swap newyork-london 12 for chicago-london 15
        check("checker rejects a heavier spanning tree", !Mst.isMinimumSpanningForest(g, new SpanningForest(wrong, 42, 1)));
        check("checker rejects too few edges", !Mst.isMinimumSpanningForest(g, new SpanningForest(k.edges().subList(0, 6), 27, 1)));

        // equal weights: both algorithms still produce minimum forests, possibly different edges
        WeightedGraph square = new WeightedGraph().addEdge("a", "b", 1).addEdge("b", "c", 1).addEdge("c", "d", 1).addEdge("d", "a", 1);
        SpanningForest ks = Mst.kruskal(square), ps = Mst.prim(square, "c");
        check("ties: kruskal weight 3", ks.totalWeight() == 3 && Mst.isMinimumSpanningForest(square, ks));
        check("ties: prim weight 3", ps.totalWeight() == 3 && Mst.isMinimumSpanningForest(square, ps));
        check("ties: kruskal keeps insertion order", ks.edges().equals(square.edges().subList(0, 3)));
        check("ties: deterministic across runs", Mst.kruskal(square).edges().equals(ks.edges()) && Mst.prim(square, "c").edges().equals(ps.edges()));

        // boundaries
        check("empty graph", Mst.kruskal(new WeightedGraph()).edges().isEmpty() && Mst.kruskal(new WeightedGraph()).componentCount() == 0);
        check("single node", Mst.kruskal(new WeightedGraph().addNode("x")).componentCount() == 1 && Mst.prim(new WeightedGraph().addNode("x"), "x").edges().isEmpty());
        WeightedGraph parallel = new WeightedGraph().addEdge("a", "b", 5).addEdge("a", "b", 3);
        check("parallel edges: lighter one chosen", Mst.kruskal(parallel).totalWeight() == 3 && Mst.prim(parallel, "a").totalWeight() == 3);

        // disconnected graph gives a forest, not an exception
        WeightedGraph two = new WeightedGraph().addEdge("a", "b", 1).addEdge("c", "d", 2).addNode("e");
        SpanningForest f = Mst.kruskal(two);
        check("forest: 3 components, 2 edges", f.componentCount() == 3 && f.edges().size() == 2 && !f.isTree());
        check("forest: prim agrees", Mst.prim(two, "a").componentCount() == 3 && Mst.prim(two, "a").totalWeight() == 3);
        check("forest passes checker", Mst.isMinimumSpanningForest(two, f));

        // randomized cross-check: Kruskal == Prim total on 30 seeded graphs, and both pass the cut-property checker
        Random rng = new Random(42);
        boolean agree = true, verified = true;
        for (int trial = 0; trial < 30; trial++) {
            WeightedGraph r = new WeightedGraph();
            int n = 4 + rng.nextInt(6);
            for (int i = 0; i < n; i++) r.addNode("n" + i);
            int m = n + rng.nextInt(n * 2);
            for (int i = 0; i < m; i++) {
                int a = rng.nextInt(n), b = rng.nextInt(n);
                if (a != b) r.addEdge("n" + a, "n" + b, 1 + rng.nextInt(20));
            }
            SpanningForest rk = Mst.kruskal(r), rp = Mst.prim(r, "n0");
            agree &= rk.totalWeight() == rp.totalWeight() && rk.componentCount() == rp.componentCount();
            verified &= Mst.isMinimumSpanningForest(r, rk) && Mst.isMinimumSpanningForest(r, rp);
        }
        check("30 random graphs: kruskal and prim agree on total", agree);
        check("30 random graphs: both pass the cut-property checker", verified);

        // clustering
        check("3 clusters", Mst.clusters(g, 3).equals(List.of(
                List.of("ashburn", "newyork"), List.of("chicago", "dallas", "phoenix", "sanjose"), List.of("london", "frankfurt"))));
        check("1 cluster is everything", Mst.clusters(g, 1).size() == 1 && Mst.clusters(g, 1).get(0).size() == 8);
        check("k >= n gives singletons", Mst.clusters(g, 10).size() == 8);
        checkThrows("k must be positive", IllegalArgumentException.class, () -> Mst.clusters(g, 0));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f2 -> System.out.println("FAIL: " + f2));
        if (!failures.isEmpty()) System.exit(1);
    }

    static List<Long> weights(SpanningForest f) {
        List<Long> w = new ArrayList<>();
        for (WeightedEdge e : f.edges()) w.add(e.weight());
        return w;
    }
}
