// Tests for phases/02-algorithms/09-shortest-paths. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Optional;

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

    /** Tiny 64-bit LCG, identical in all three languages, so the random graph is the same everywhere. */
    static final class SeededRandom {
        private long state;
        SeededRandom(long seed) { state = seed; }
        int nextInt(int bound) {
            state = state * 6364136223846793005L + 1442695040888963407L;
            return (int) Long.remainderUnsigned(state >>> 33, bound);
        }
    }

    static WeightedGraph spreads() {
        WeightedGraph g = new WeightedGraph();
        g.addEdge("USD", "EUR", 20);
        g.addEdge("EUR", "JPY", 20);
        g.addEdge("USD", "JPY", 100);
        g.addEdge("EUR", "GBP", 20);
        g.addEdge("GBP", "JPY", 15);
        g.addNode("CHF");
        return g;
    }

    public static void main(String[] args) {
        // Dijkstra
        PathResult d = Dijkstra.shortestPaths(spreads(), "USD");
        check("dijkstra USD->JPY is 40 via EUR", d.distance().get("JPY") == 40 && d.pathTo("JPY").equals(List.of("USD", "EUR", "JPY")));
        check("dijkstra GBP distance", d.distance().get("GBP") == 40 && d.pathTo("GBP").equals(List.of("USD", "EUR", "GBP")));
        check("dijkstra source distance 0 and path to self", d.distance().get("USD") == 0 && d.pathTo("USD").equals(List.of("USD")));
        check("dijkstra unreachable node absent", !d.reached("CHF") && d.pathTo("CHF").isEmpty());
        WeightedGraph relax = new WeightedGraph();
        relax.addEdge("A", "B", 10);
        relax.addEdge("A", "C", 1);
        relax.addEdge("C", "B", 2);
        PathResult r = Dijkstra.shortestPaths(relax, "A");
        check("dijkstra decreaseKey replaces a worse tentative distance", r.distance().get("B") == 3 && r.pathTo("B").equals(List.of("A", "C", "B")));
        WeightedGraph zero = new WeightedGraph();
        zero.addEdge("A", "B", 0);
        zero.addEdge("B", "C", 0);
        check("dijkstra handles zero-weight edges", Dijkstra.shortestPaths(zero, "A").distance().get("C") == 0);
        WeightedGraph negative = new WeightedGraph();
        negative.addEdge("A", "B", 5);
        negative.addEdge("A", "C", 2);
        negative.addEdge("C", "B", -4);
        checkThrows("dijkstra rejects negative weights", IllegalArgumentException.class, () -> Dijkstra.shortestPaths(negative, "A"));
        checkThrows("dijkstra rejects unknown source", IllegalArgumentException.class, () -> Dijkstra.shortestPaths(spreads(), "XXX"));

        // Bellman-Ford agrees with Dijkstra on non-negative weights, including a seeded random graph
        BellmanFordResult bf = BellmanFord.run(spreads(), "USD");
        check("bellman-ford matches dijkstra on spreads", bf.paths().distance().equals(d.distance()) && bf.negativeCycle().isEmpty());
        SeededRandom rng = new SeededRandom(42);
        WeightedGraph random = new WeightedGraph();
        for (int i = 0; i < 40; i++) random.addNode("n" + i);
        for (int i = 0; i < 160; i++) random.addEdge("n" + rng.nextInt(40), "n" + rng.nextInt(40), rng.nextInt(100));
        PathResult dr = Dijkstra.shortestPaths(random, "n0");
        BellmanFordResult br = BellmanFord.run(random, "n0");
        check("random graph: same distances from both algorithms", dr.distance().equals(br.paths().distance()));
        check("random graph: dijkstra reached more than the source", dr.distance().size() > 1);
        boolean pathsConsistent = true;
        for (String node : dr.distance().keySet()) {
            List<String> path = dr.pathTo(node);
            long sum = 0;
            for (int i = 0; i + 1 < path.size(); i++) {
                long w = Long.MAX_VALUE;
                for (Edge e : random.outEdges(path.get(i))) if (e.to().equals(path.get(i + 1))) w = Math.min(w, e.weight());
                sum += w;
            }
            if (sum != dr.distance().get(node)) pathsConsistent = false;
        }
        check("random graph: every reconstructed path sums to its distance", pathsConsistent);

        // Bellman-Ford with negative edges and negative cycles
        BellmanFordResult neg = BellmanFord.run(negative, "A");
        check("bellman-ford handles a negative edge (A->C->B = -2)", neg.paths().distance().get("B") == -2 && neg.negativeCycle().isEmpty());
        WeightedGraph cyc = new WeightedGraph();
        cyc.addEdge("A", "B", 1);
        cyc.addEdge("B", "C", -3);
        cyc.addEdge("C", "A", 1);
        Optional<List<String>> cycle = BellmanFord.run(cyc, "A").negativeCycle();
        check("negative cycle detected", cycle.isPresent());
        check("negative cycle is a closed walk of 3 edges", cycle.get().size() == 4 && cycle.get().get(0).equals(cycle.get().get(3)));
        check("negative cycle contains A, B, C", cycle.get().containsAll(List.of("A", "B", "C")));
        WeightedGraph unreachableCycle = new WeightedGraph();
        unreachableCycle.addEdge("S", "T", 1);
        unreachableCycle.addEdge("P", "Q", -5);
        unreachableCycle.addEdge("Q", "P", 1);
        check("negative cycle not reachable from the source is not reported", BellmanFord.run(unreachableCycle, "S").negativeCycle().isEmpty());
        check("bellman-ford early exit on a DAG still correct", BellmanFord.run(spreads(), "USD").paths().pathTo("JPY").equals(List.of("USD", "EUR", "JPY")));

        // FX weights
        check("weight of rate 1.0 is 0", FxGraph.weightOf(FxGraph.RATE_SCALE) == 0);
        check("rate below 1 has positive weight", FxGraph.weightOf(91_816_000L) == 85384);
        check("rate above 1 has negative weight", FxGraph.weightOf(108_478_260L) == -81380);
        check("weight of 150 (USD->JPY mid) is -ln(150) micro-nats", FxGraph.weightOf(15_000_000_000L) == -5010635);
        long wa = FxGraph.weightOf(50_000_000L), wb = FxGraph.weightOf(200_000_000L);
        check("weights of reciprocal rates cancel", wa + wb == 0);

        // Best path and exact conversion
        List<Rate> rates = Main.market();
        Map<String, Currency> cur = Main.currencies();
        List<String> best = FxGraph.bestConversionPath(rates, "USD", "JPY");
        check("best USD->JPY path goes through EUR", best.equals(List.of("USD", "EUR", "JPY")));
        Money tenK = Money.ofMinor(1_000_000, Currency.USD);
        check("10,000 USD along best path = 1,493,975 JPY", FxGraph.convertAlong(tenK, best, rates, cur).equals(Money.ofMinor(1_493_975, Currency.JPY)));
        check("10,000 USD on direct quote = 1,485,000 JPY", FxGraph.convertAlong(tenK, List.of("USD", "JPY"), rates, cur).equals(Money.ofMinor(1_485_000, Currency.JPY)));
        check("convert handles exponent change USD->JPY", Money.ofMinor(10_000, Currency.USD).convert(15_000_000_000L, Currency.JPY, Rounding.HALF_EVEN).equals(Money.ofMinor(15_000, Currency.JPY)));
        check("convert handles exponent change JPY->USD", Money.ofMinor(15_000, Currency.JPY).convert(666_667L, Currency.USD, Rounding.HALF_EVEN).equals(Money.ofMinor(10_000, Currency.USD)));
        checkThrows("convertAlong rejects a path that does not start at the amount's currency", IllegalArgumentException.class,
                () -> FxGraph.convertAlong(tenK, List.of("EUR", "USD"), rates, cur));
        checkThrows("rate must be positive", IllegalArgumentException.class, () -> new Rate("A", "B", 0));

        // Arbitrage
        check("consistent market has no arbitrage", FxGraph.arbitrage(rates).isEmpty());
        List<Rate> stale = new ArrayList<>(rates.subList(0, 8));
        stale.add(new Rate("GBP", "USD", 128_000_000L));
        Optional<List<String>> arb = FxGraph.arbitrage(stale);
        check("stale quote creates an arbitrage cycle", arb.isPresent());
        check("arbitrage cycle is USD/EUR/GBP", arb.get().size() == 4 && arb.get().containsAll(List.of("USD", "EUR", "GBP")));
        FxGraph.CycleProduct p = FxGraph.cycleProduct(stale, arb.get());
        check("exact product exceeds one", p.exceedsOne());
        check("gain is 86 basis points", p.gainBasisPoints() == 86);
        List<String> fromUsd = FxGraph.rotateTo(arb.get(), "USD");
        check("rotated cycle starts and ends at USD", fromUsd.get(0).equals("USD") && fromUsd.get(3).equals("USD") && fromUsd.size() == 4);
        check("10,000 USD around the loop = 10,086.89 USD", FxGraph.convertAlong(tenK, fromUsd, stale, cur).equals(Money.ofMinor(1_008_689, Currency.USD)));
        checkThrows("bestConversionPath refuses a market with arbitrage", IllegalStateException.class, () -> FxGraph.bestConversionPath(stale, "USD", "JPY"));

        // Rounding artefact: 0.16 * 0.25 * 25 = 1 exactly, but the rounded weights sum to -1 micro-nat
        List<Rate> exact = List.of(new Rate("A", "B", 16_000_000L), new Rate("B", "C", 25_000_000L), new Rate("C", "A", 2_500_000_000L));
        long sum = FxGraph.weightOf(16_000_000L) + FxGraph.weightOf(25_000_000L) + FxGraph.weightOf(2_500_000_000L);
        check("rounded weights of an exact-1 cycle sum to -1", sum == -1);
        check("raw bellman-ford reports that cycle", BellmanFord.run(FxGraph.fromRates(exact), "A").negativeCycle().isPresent());
        check("arbitrage() rejects it after the exact product check", FxGraph.arbitrage(exact).isEmpty());
        check("exact product of that cycle equals one", !FxGraph.cycleProduct(exact, List.of("A", "B", "C", "A")).exceedsOne()
                && FxGraph.cycleProduct(exact, List.of("A", "B", "C", "A")).gainBasisPoints() == 0);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
