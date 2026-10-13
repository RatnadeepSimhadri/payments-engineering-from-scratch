// Lesson: phases/02-algorithms/09-shortest-paths/docs/en.md
// Shortest Paths: Dijkstra, Bellman-Ford, and Negative Cycles
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Dijkstra (1959), "A note on two problems in connexion with graphs"; Bellman (1958), "On a routing problem";
// CLRS ch. 24. IndexedMinHeap copied from phases/01-complexity-and-core-structures/09-heaps-and-priority-queues;
// Money copied from phases/10-money-and-ledgers/01-representing-money. Java 21, standard library only.

import java.math.BigInteger;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.NoSuchElementException;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;

// ---------------------------------------------------------------------------------------------------------
// Money (trimmed copy of Phase 10, Lesson 01): integer minor units, exact rational multiplication.
// ---------------------------------------------------------------------------------------------------------

record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency EUR = new Currency("EUR", 2);
    static final Currency GBP = new Currency("GBP", 2);
    static final Currency JPY = new Currency("JPY", 0);

    Currency {
        if (code == null || code.length() != 3) throw new IllegalArgumentException("currency code must be three letters");
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4");
    }

    long minorUnitsPerMajor() {
        long scale = 1;
        for (int i = 0; i < exponent; i++) scale *= 10;
        return scale;
    }
}

enum Rounding { HALF_EVEN, HALF_UP, DOWN, UP }

record Money(long minorUnits, Currency currency) {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    static long divideRounded(long dividend, long divisor, Rounding rounding) {
        long quotient = dividend / divisor;
        long remainder = dividend % divisor;
        if (remainder == 0) return quotient;
        int sign = (dividend < 0) ^ (divisor < 0) ? -1 : 1;
        long twiceRemainder = Math.abs(remainder) * 2;
        long absDivisor = Math.abs(divisor);
        return switch (rounding) {
            case DOWN -> quotient;
            case UP -> quotient + sign;
            case HALF_UP -> twiceRemainder >= absDivisor ? quotient + sign : quotient;
            case HALF_EVEN -> {
                if (twiceRemainder > absDivisor) yield quotient + sign;
                if (twiceRemainder < absDivisor) yield quotient;
                yield (quotient % 2 == 0) ? quotient : quotient + sign;
            }
        };
    }

    /**
     * Convert into another currency at an exact scaled rate (rate = scaledRate / FxGraph.RATE_SCALE), with one
     * rounding at the end. Minor-unit exponents may differ (USD cents to JPY yen), so both scales are applied.
     */
    Money convert(long scaledRate, Currency to, Rounding rounding) {
        long numerator = Math.multiplyExact(Math.multiplyExact(minorUnits, scaledRate), to.minorUnitsPerMajor());
        long denominator = Math.multiplyExact(FxGraph.RATE_SCALE, currency.minorUnitsPerMajor());
        return new Money(divideRounded(numerator, denominator, rounding), to);
    }

    @Override
    public String toString() {
        long magnitude = Math.abs(minorUnits);
        long scale = currency.minorUnitsPerMajor();
        String text = Long.toString(magnitude / scale);
        if (currency.exponent() > 0) {
            String frac = Long.toString(magnitude % scale);
            text = text + "." + "0".repeat(currency.exponent() - frac.length()) + frac;
        }
        return (minorUnits < 0 ? "-" : "") + text + " " + currency.code();
    }
}

// ---------------------------------------------------------------------------------------------------------
// IndexedMinHeap (copied from Phase 1, Lesson 09): the priority queue Dijkstra needs, with decreaseKey.
// ---------------------------------------------------------------------------------------------------------

record Entry<K>(K key, long priority, long seq) {
    static <K> int compare(Entry<K> a, Entry<K> b) {
        int c = Long.compare(a.priority, b.priority);
        return c != 0 ? c : Long.compare(a.seq, b.seq);
    }
}

final class IndexedMinHeap<K> {
    private final List<Entry<K>> entries = new ArrayList<>();
    private final Map<K, Integer> index = new HashMap<>();
    private long nextSeq = 0;

    void push(K key, long priority) {
        if (index.containsKey(key)) throw new IllegalArgumentException("key already present: " + key);
        entries.add(new Entry<>(key, priority, nextSeq++));
        index.put(key, entries.size() - 1);
        siftUp(entries.size() - 1);
    }

    void decreaseKey(K key, long newPriority) {
        Integer i = index.get(key);
        if (i == null) throw new NoSuchElementException("key not present: " + key);
        Entry<K> current = entries.get(i);
        if (newPriority > current.priority()) {
            throw new IllegalArgumentException("decreaseKey cannot raise " + key + " from " + current.priority() + " to " + newPriority);
        }
        entries.set(i, new Entry<>(key, newPriority, current.seq()));
        siftUp(i);
    }

    Entry<K> pop() {
        if (entries.isEmpty()) throw new NoSuchElementException("heap is empty");
        Entry<K> top = entries.get(0);
        Entry<K> last = entries.remove(entries.size() - 1);
        index.remove(top.key());
        if (!entries.isEmpty()) {
            entries.set(0, last);
            index.put(last.key(), 0);
            siftDown(0);
        }
        return top;
    }

    boolean contains(K key) { return index.containsKey(key); }
    int size() { return entries.size(); }
    boolean isEmpty() { return entries.isEmpty(); }

    private void siftUp(int i) {
        while (i > 0) {
            int parent = (i - 1) / 2;
            if (Entry.compare(entries.get(parent), entries.get(i)) <= 0) return;
            swap(parent, i);
            i = parent;
        }
    }

    private void siftDown(int i) {
        int n = entries.size();
        while (true) {
            int left = 2 * i + 1, right = left + 1, smallest = i;
            if (left < n && Entry.compare(entries.get(left), entries.get(smallest)) < 0) smallest = left;
            if (right < n && Entry.compare(entries.get(right), entries.get(smallest)) < 0) smallest = right;
            if (smallest == i) return;
            swap(i, smallest);
            i = smallest;
        }
    }

    private void swap(int a, int b) {
        Entry<K> ea = entries.get(a), eb = entries.get(b);
        entries.set(a, eb);
        entries.set(b, ea);
        index.put(eb.key(), a);
        index.put(ea.key(), b);
    }
}

// ---------------------------------------------------------------------------------------------------------
// Weighted graph and the two shortest-path algorithms.
// ---------------------------------------------------------------------------------------------------------

/** A directed edge with an integer weight. Weights are longs on purpose: sums are exact and associative. */
record Edge(String from, String to, long weight) {}

final class WeightedGraph {
    private final Map<String, List<Edge>> adjacency = new LinkedHashMap<>();
    private final List<Edge> edges = new ArrayList<>();

    void addNode(String id) { adjacency.computeIfAbsent(id, k -> new ArrayList<>()); }

    void addEdge(String from, String to, long weight) {
        addNode(from);
        addNode(to);
        Edge e = new Edge(from, to, weight);
        adjacency.get(from).add(e);
        edges.add(e);
    }

    boolean hasNode(String id) { return adjacency.containsKey(id); }
    Set<String> nodes() { return adjacency.keySet(); }
    List<Edge> outEdges(String id) {
        List<Edge> out = adjacency.get(id);
        if (out == null) throw new IllegalArgumentException("unknown node: " + id);
        return out;
    }
    List<Edge> edges() { return edges; }
    int nodeCount() { return adjacency.size(); }
}

/** Distances and parents from one source; a path is read back through the parents. */
record PathResult(Map<String, Long> distance, Map<String, String> parent) {
    boolean reached(String id) { return distance.containsKey(id); }

    List<String> pathTo(String target) {
        if (!reached(target)) return List.of();
        List<String> path = new ArrayList<>();
        for (String at = target; at != null; at = parent.get(at)) path.add(0, at);
        return path;
    }
}

final class Dijkstra {
    /**
     * Single-source shortest paths for NON-NEGATIVE weights. Invariant: when a node is popped from the heap its
     * distance is final, because every other reachable node has distance >= the popped one and edges only add.
     * O((V + E) log V) with the indexed heap: each node pushed once, each edge relaxes at most once.
     */
    static PathResult shortestPaths(WeightedGraph g, String source) {
        if (!g.hasNode(source)) throw new IllegalArgumentException("unknown node: " + source);
        for (Edge e : g.edges()) {
            if (e.weight() < 0) throw new IllegalArgumentException("Dijkstra requires non-negative weights; " + e.from() + "->" + e.to() + " is " + e.weight());
        }
        Map<String, Long> distance = new LinkedHashMap<>();
        Map<String, String> parent = new HashMap<>();
        Map<String, Long> tentative = new HashMap<>();
        IndexedMinHeap<String> heap = new IndexedMinHeap<>();
        tentative.put(source, 0L);
        heap.push(source, 0L);
        while (!heap.isEmpty()) {
            Entry<String> top = heap.pop();
            String u = top.key();
            distance.put(u, top.priority());                // settled: never changes again
            for (Edge e : g.outEdges(u)) {
                if (distance.containsKey(e.to())) continue;  // already settled; a shorter route cannot exist
                long candidate = Math.addExact(top.priority(), e.weight());
                Long known = tentative.get(e.to());
                if (known == null) {
                    tentative.put(e.to(), candidate);
                    parent.put(e.to(), u);
                    heap.push(e.to(), candidate);
                } else if (candidate < known) {
                    tentative.put(e.to(), candidate);
                    parent.put(e.to(), u);
                    heap.decreaseKey(e.to(), candidate);
                }
            }
        }
        return new PathResult(distance, parent);
    }
}

/** Bellman-Ford output: shortest paths if there is no negative cycle reachable from the source, else the cycle. */
record BellmanFordResult(PathResult paths, Optional<List<String>> negativeCycle) {}

final class BellmanFord {
    /**
     * Single-source shortest paths for ANY weights, O(V * E). Relax every edge V-1 times; a shortest path has at
     * most V-1 edges, so after round k every node whose shortest path has k edges is correct. If a V-th round
     * still relaxes an edge, a negative cycle is reachable, and the parent pointers lead into it.
     */
    static BellmanFordResult run(WeightedGraph g, String source) {
        if (!g.hasNode(source)) throw new IllegalArgumentException("unknown node: " + source);
        Map<String, Long> distance = new LinkedHashMap<>();
        Map<String, String> parent = new HashMap<>();
        distance.put(source, 0L);
        int n = g.nodeCount();
        String relaxedInLastRound = null;
        for (int round = 1; round <= n; round++) {
            relaxedInLastRound = null;
            for (Edge e : g.edges()) {
                Long du = distance.get(e.from());
                if (du == null) continue;                        // unreachable so far
                long candidate = Math.addExact(du, e.weight());
                Long dv = distance.get(e.to());
                if (dv == null || candidate < dv) {
                    distance.put(e.to(), candidate);
                    parent.put(e.to(), e.from());
                    relaxedInLastRound = e.to();
                }
            }
            if (relaxedInLastRound == null) break;               // early exit: a full round changed nothing
        }
        if (relaxedInLastRound == null) return new BellmanFordResult(new PathResult(distance, parent), Optional.empty());
        // Round n still relaxed: walk parents n times to be sure we are inside the cycle, then collect it.
        String x = relaxedInLastRound;
        for (int i = 0; i < n; i++) x = parent.get(x);
        List<String> cycle = new ArrayList<>();
        for (String at = x; ; at = parent.get(at)) {
            cycle.add(0, at);                                     // parents point backwards; prepend to get forward order
            if (at.equals(x) && cycle.size() > 1) break;
        }
        return new BellmanFordResult(new PathResult(distance, parent), Optional.of(cycle));
    }
}

// ---------------------------------------------------------------------------------------------------------
// FX: rates as scaled integers, weights as integer-scaled negative logs, arbitrage as a negative cycle.
// ---------------------------------------------------------------------------------------------------------

/** One quote: 1 unit of `from` buys scaledRate / RATE_SCALE units of `to`. */
record Rate(String from, String to, long scaledRate) {
    Rate {
        if (scaledRate <= 0) throw new IllegalArgumentException("rate must be positive: " + from + "->" + to);
    }
}

final class FxGraph {
    /** Rates carry 8 decimals: 0.92 is 92_000_000. Enough for every quoted FX pair; exact in a long. */
    static final long RATE_SCALE = 100_000_000L;
    /** Weights are -ln(rate) in micro-nats, rounded once. 1 micro-nat is 0.0001% of rate: below any quote's precision. */
    static final long WEIGHT_SCALE = 1_000_000L;
    static final String SOURCE = "__source__";

    /**
     * The only floating-point operation in the lesson. ln turns a product of rates into a sum of weights; the
     * minus sign turns "best rate" into "shortest path"; the scale turns the double into a long so that every
     * sum is exact, associative, and identical across Java, Python, and Go. A cycle whose rates multiply to
     * more than 1 has total weight < 0 up to rounding of +-0.5 micro-nat per edge, so every detected cycle is
     * re-checked exactly with cycleProduct before anyone calls it arbitrage.
     */
    static long weightOf(long scaledRate) {
        // Math.round rounds ties up; Python's round() rounds ties to even; Go's math.Round rounds ties away from
        // zero. No rate in this lesson lands on an exact tie, so the three agree. A production router that must
        // agree across languages should round the scaled value by hand with one explicit rule.
        double rate = (double) scaledRate / RATE_SCALE;
        return Math.round(-Math.log(rate) * WEIGHT_SCALE);
    }

    static WeightedGraph fromRates(List<Rate> rates) {
        WeightedGraph g = new WeightedGraph();
        for (Rate r : rates) g.addEdge(r.from(), r.to(), weightOf(r.scaledRate()));
        return g;
    }

    /** Best conversion path (max product of rates = min sum of -log weights). Bellman-Ford, because weights can be negative. */
    static List<String> bestConversionPath(List<Rate> rates, String from, String to) {
        BellmanFordResult r = BellmanFord.run(fromRates(rates), from);
        if (r.negativeCycle().isPresent()) throw new IllegalStateException("rates contain an arbitrage cycle: " + r.negativeCycle().get());
        return r.paths().pathTo(to);
    }

    /** Any negative cycle in the whole graph: add a virtual source with zero-weight edges to every currency. */
    static Optional<List<String>> arbitrage(List<Rate> rates) {
        WeightedGraph g = fromRates(rates);
        for (String node : new ArrayList<>(g.nodes())) g.addEdge(SOURCE, node, 0);
        Optional<List<String>> cycle = BellmanFord.run(g, SOURCE).negativeCycle();
        if (cycle.isPresent() && !cycleProduct(rates, cycle.get()).exceedsOne()) {
            return Optional.empty();                             // rounding artefact, not arbitrage: the exact product is <= 1
        }
        return cycle;
    }

    /** Exact rational product of the rates along a closed walk: numerator / RATE_SCALE^k, as BigIntegers. */
    record CycleProduct(BigInteger numerator, BigInteger denominator) {
        boolean exceedsOne() { return numerator.compareTo(denominator) > 0; }

        /** Gain in basis points, rounded toward zero: (num/den - 1) * 10000. */
        long gainBasisPoints() {
            return numerator.subtract(denominator).multiply(BigInteger.valueOf(10_000)).divide(denominator).longValueExact();
        }
    }

    static CycleProduct cycleProduct(List<Rate> rates, List<String> closedWalk) {
        BigInteger num = BigInteger.ONE, den = BigInteger.ONE;
        for (int i = 0; i + 1 < closedWalk.size(); i++) {
            num = num.multiply(BigInteger.valueOf(rateBetween(rates, closedWalk.get(i), closedWalk.get(i + 1))));
            den = den.multiply(BigInteger.valueOf(RATE_SCALE));
        }
        return new CycleProduct(num, den);
    }

    /** Rotate a closed walk so it starts (and ends) at the given node: [EUR, GBP, USD, EUR] -> [USD, EUR, GBP, USD]. */
    static List<String> rotateTo(List<String> closedWalk, String start) {
        int k = closedWalk.indexOf(start);
        if (k < 0) throw new NoSuchElementException(start + " is not on the cycle " + closedWalk);
        List<String> out = new ArrayList<>();
        int len = closedWalk.size() - 1;
        for (int i = 0; i <= len; i++) out.add(closedWalk.get((k + i) % len));
        return out;
    }

    static long rateBetween(List<Rate> rates, String from, String to) {
        for (Rate r : rates) if (r.from().equals(from) && r.to().equals(to)) return r.scaledRate();
        throw new NoSuchElementException("no rate " + from + "->" + to);
    }

    /** Convert an amount along a path, rounding HALF_EVEN at each hop as a real multi-leg conversion would. */
    static Money convertAlong(Money amount, List<String> path, List<Rate> rates, Map<String, Currency> currencies) {
        if (path.isEmpty() || !amount.currency().code().equals(path.get(0))) {
            throw new IllegalArgumentException("amount is " + amount.currency().code() + " but path starts at " + path);
        }
        Money current = amount;
        for (int i = 0; i + 1 < path.size(); i++) {
            current = current.convert(rateBetween(rates, path.get(i), path.get(i + 1)), currencies.get(path.get(i + 1)), Rounding.HALF_EVEN);
        }
        return current;
    }
}

public final class Main {
    /** Market: mid rates with a 0.2% spread applied, except USD->JPY which is quoted wide (1%). */
    static List<Rate> market() {
        return List.of(
                new Rate("USD", "EUR", 91_816_000L),     // 0.92 * 0.998
                new Rate("EUR", "USD", 108_478_260L),    // 1/0.92 * 0.998
                new Rate("EUR", "GBP", 85_828_000L),     // 0.86 * 0.998
                new Rate("GBP", "EUR", 116_046_510L),    // 1/0.86 * 0.998
                new Rate("USD", "JPY", 14_850_000_000L), // 150 * 0.99   (wide)
                new Rate("JPY", "USD", 665_333L),        // 1/150 * 0.998
                new Rate("EUR", "JPY", 16_271_400_000L), // 163.04 * 0.998
                new Rate("JPY", "EUR", 612_060L),        // 1/163.04 * 0.998
                new Rate("GBP", "USD", 126_137_000L));   // 1.2639 * 0.998
    }

    static Map<String, Currency> currencies() {
        return Map.of("USD", Currency.USD, "EUR", Currency.EUR, "GBP", Currency.GBP, "JPY", Currency.JPY);
    }

    public static void main(String[] args) {
        System.out.println("== Rates to weights: -ln(rate) in micro-nats ==");
        for (Rate r : market().subList(0, 5)) {
            System.out.printf("%s->%s rate=%d/1e8 weight=%d%n", r.from(), r.to(), r.scaledRate(), FxGraph.weightOf(r.scaledRate()));
        }

        System.out.println("\n== Dijkstra on non-negative spread costs (basis points) ==");
        WeightedGraph spreads = new WeightedGraph();
        spreads.addEdge("USD", "EUR", 20);
        spreads.addEdge("EUR", "JPY", 20);
        spreads.addEdge("USD", "JPY", 100);
        spreads.addEdge("EUR", "GBP", 20);
        spreads.addEdge("GBP", "JPY", 15);
        PathResult d = Dijkstra.shortestPaths(spreads, "USD");
        System.out.println("USD->JPY cheapest spread: " + d.distance().get("JPY") + " bps via " + d.pathTo("JPY"));
        System.out.println("all distances: " + d.distance());

        System.out.println("\n== Bellman-Ford on -log weights: best conversion path ==");
        List<Rate> rates = market();
        List<String> best = FxGraph.bestConversionPath(rates, "USD", "JPY");
        Money tenThousand = Money.ofMinor(1_000_000, Currency.USD);
        System.out.println("best USD->JPY path: " + best);
        System.out.println("  10,000.00 USD along best path  = " + FxGraph.convertAlong(tenThousand, best, rates, currencies()));
        System.out.println("  10,000.00 USD on the direct quote = " + FxGraph.convertAlong(tenThousand, List.of("USD", "JPY"), rates, currencies()));
        System.out.println("arbitrage in consistent market: " + FxGraph.arbitrage(rates).map(Object::toString).orElse("none"));

        System.out.println("\n== A stale GBP->USD quote creates a negative cycle ==");
        List<Rate> stale = new ArrayList<>(rates.subList(0, 8));
        stale.add(new Rate("GBP", "USD", 128_000_000L));          // 1.28 instead of 1.2614
        Optional<List<String>> cycle = FxGraph.arbitrage(stale);
        System.out.println("arbitrage cycle: " + cycle.orElse(List.of()));
        if (cycle.isPresent()) {
            FxGraph.CycleProduct p = FxGraph.cycleProduct(stale, cycle.get());
            System.out.println("exact product of rates along the cycle = " + p.numerator() + " / " + p.denominator());
            System.out.println("gain = " + p.gainBasisPoints() + " basis points per loop");
            List<String> fromUsd = FxGraph.rotateTo(cycle.get(), "USD");
            System.out.println("10,000.00 USD around " + fromUsd + " = " + FxGraph.convertAlong(tenThousand, fromUsd, stale, currencies()));
        }

        System.out.println("\n== Why Dijkstra cannot be used on these weights ==");
        try {
            Dijkstra.shortestPaths(FxGraph.fromRates(rates), "USD");
        } catch (IllegalArgumentException e) {
            System.out.println("rejected: " + e.getMessage());
        }
    }
}
