// Lesson: phases/02-algorithms/07-greedy-algorithms/docs/en.md
// Greedy Algorithms and Exchange Arguments
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Huffman, "A Method for the Construction of Minimum-Redundancy Codes" (Proc. IRE, 1952); Kleinberg & Tardos,
// Algorithm Design, ch. 4 (interval scheduling, exchange arguments); Pearson, "A polynomial-time algorithm for the
// change-making problem" (2005, canonical coin systems). Java 21, standard library only.

import java.util.ArrayList;
import java.util.Arrays;
import java.util.Comparator;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.PriorityQueue;
import java.util.TreeMap;

/** ISO 4217 currency: a three-letter code and the number of decimal places in its minor unit. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);

    Currency {
        if (code == null || code.length() != 3) throw new IllegalArgumentException("currency code: " + code);
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4: " + exponent);
    }
}

/** An exact amount of one currency as a signed count of minor units (Phase 10, Lesson 01). */
record Money(long minorUnits, Currency currency) implements Comparable<Money> {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    @Override public int compareTo(Money other) {
        if (!currency.equals(other.currency)) throw new IllegalArgumentException("cannot compare " + currency.code() + " with " + other.currency.code());
        return Long.compare(minorUnits, other.minorUnits);
    }

    @Override public String toString() {
        long scale = 1;
        for (int i = 0; i < currency.exponent(); i++) scale *= 10;
        long whole = Math.abs(minorUnits) / scale, frac = Math.abs(minorUnits) % scale;
        String sign = minorUnits < 0 ? "-" : "";
        if (currency.exponent() == 0) return sign + whole + " " + currency.code();
        return String.format("%s%d.%0" + currency.exponent() + "d %s", sign, whole, frac, currency.code());
    }
}

/** A half-open time interval [start, end): a settlement window, a maintenance slot, a batch job's run. */
record Interval(String name, long start, long end) {
    Interval {
        if (end <= start) throw new IllegalArgumentException("interval must have end > start: " + name);
    }
}

/** Greedy interval problems. Both are provably optimal by exchange arguments (see docs). */
final class IntervalScheduling {
    private IntervalScheduling() {}

    /** Maximum number of pairwise non-overlapping intervals: always take the one that ends earliest. O(n log n). */
    static List<Interval> maxNonOverlapping(List<Interval> intervals) {
        List<Interval> sorted = new ArrayList<>(intervals);
        sorted.sort(Comparator.comparingLong(Interval::end).thenComparingLong(Interval::start));
        List<Interval> chosen = new ArrayList<>();
        long lastEnd = Long.MIN_VALUE;
        for (Interval iv : sorted) {
            if (iv.start() >= lastEnd) { chosen.add(iv); lastEnd = iv.end(); }   // compatible: take it
        }
        return chosen;
    }

    /** The tempting wrong greedy: take the shortest interval first. Kept to show it fail. */
    static List<Interval> shortestFirst(List<Interval> intervals) {
        List<Interval> sorted = new ArrayList<>(intervals);
        sorted.sort(Comparator.comparingLong((Interval iv) -> iv.end() - iv.start()).thenComparingLong(Interval::start));
        List<Interval> chosen = new ArrayList<>();
        for (Interval iv : sorted) {
            boolean compatible = true;
            for (Interval c : chosen) if (iv.start() < c.end() && c.start() < iv.end()) { compatible = false; break; }
            if (compatible) chosen.add(iv);
        }
        return chosen;
    }

    /**
     * Minimum number of resources (rooms, settlement lanes, worker threads) so that no two overlapping intervals
     * share one. Sort by start; a min-heap of end times tells whether the earliest-finishing lane is free. O(n log n).
     * The answer equals the maximum overlap depth, which is the lower bound, so this is optimal.
     */
    static int minResources(List<Interval> intervals) {
        List<Interval> sorted = new ArrayList<>(intervals);
        sorted.sort(Comparator.comparingLong(Interval::start).thenComparingLong(Interval::end));
        PriorityQueue<Long> ends = new PriorityQueue<>();
        for (Interval iv : sorted) {
            if (!ends.isEmpty() && ends.peek() <= iv.start()) ends.poll();   // reuse the lane that frees up first
            ends.add(iv.end());
        }
        return ends.size();
    }
}

/** Huffman coding: a greedy that is optimal because merging the two rarest symbols is always safe. */
final class Huffman {
    private Huffman() {}

    /** A node of the code tree. Leaves carry a symbol; internal nodes carry the sum of their children's weights. */
    record Node(long weight, int symbol, Node left, Node right, long order) {
        boolean isLeaf() { return left == null; }
    }

    /** Build the tree. `order` breaks weight ties by creation sequence so the tree, and the code, are deterministic. */
    static Node build(Map<Integer, Long> frequencies) {
        if (frequencies.isEmpty()) throw new IllegalArgumentException("no symbols");
        PriorityQueue<Node> pq = new PriorityQueue<>(Comparator.comparingLong(Node::weight).thenComparingLong(Node::order));
        long order = 0;
        for (Map.Entry<Integer, Long> e : new TreeMap<>(frequencies).entrySet()) {
            if (e.getValue() <= 0) throw new IllegalArgumentException("frequency must be positive: " + e.getKey());
            pq.add(new Node(e.getValue(), e.getKey(), null, null, order++));
        }
        if (pq.size() == 1) {   // a one-symbol alphabet still needs a one-bit code
            Node only = pq.poll();
            return new Node(only.weight(), -1, only, null, order);
        }
        while (pq.size() > 1) {
            Node a = pq.poll(), b = pq.poll();                             // the two lightest: the greedy choice
            pq.add(new Node(a.weight() + b.weight(), -1, a, b, order++));
        }
        return pq.poll();
    }

    /** Symbol -> bit string. Left edges are 0, right edges are 1. */
    static Map<Integer, String> codes(Node root) {
        Map<Integer, String> out = new HashMap<>();
        assign(root, "", out);
        return out;
    }

    private static void assign(Node n, String prefix, Map<Integer, String> out) {
        if (n.isLeaf()) { out.put(n.symbol(), prefix.isEmpty() ? "0" : prefix); return; }
        assign(n.left(), prefix + "0", out);
        if (n.right() != null) assign(n.right(), prefix + "1", out);
    }

    /** Total encoded length in bits = sum over symbols of frequency * code length. */
    static long encodedBits(Map<Integer, Long> frequencies, Map<Integer, String> codes) {
        long bits = 0;
        for (Map.Entry<Integer, Long> e : frequencies.entrySet()) bits += e.getValue() * codes.get(e.getKey()).length();
        return bits;
    }

    static String encode(int[] symbols, Map<Integer, String> codes) {
        StringBuilder sb = new StringBuilder();
        for (int s : symbols) sb.append(codes.get(s));
        return sb.toString();
    }

    /** Decode by walking the tree; a prefix code needs no separators. */
    static int[] decode(String bits, Node root) {
        List<Integer> out = new ArrayList<>();
        Node cur = root;
        for (int i = 0; i < bits.length(); i++) {
            cur = bits.charAt(i) == '0' ? cur.left() : cur.right();
            if (cur == null) throw new IllegalArgumentException("invalid bit string");
            if (cur.isLeaf()) { out.add(cur.symbol()); cur = root; }
        }
        if (cur != root) throw new IllegalArgumentException("truncated bit string");
        return out.stream().mapToInt(Integer::intValue).toArray();
    }

    /** True when no code is a prefix of another: the property that makes decoding unambiguous. */
    static boolean isPrefixFree(Map<Integer, String> codes) {
        List<String> cs = new ArrayList<>(codes.values());
        for (int i = 0; i < cs.size(); i++)
            for (int j = 0; j < cs.size(); j++)
                if (i != j && cs.get(j).startsWith(cs.get(i))) return false;
        return true;
    }
}

/**
 * Change-making: pay an amount with the fewest coins or notes from a denomination set.
 * Greedy (largest denomination first) is optimal for canonical systems (USD, EUR, INR) and wrong for others.
 */
final class ChangeMaking {
    private ChangeMaking() {}

    /** Greedy: largest first. Returns counts per denomination (same order as input), or null if the amount cannot be made. */
    static long[] greedy(long[] denominations, long amount) {
        long[] sorted = denominations.clone();
        Arrays.sort(sorted);
        long[] count = new long[denominations.length];
        long remaining = amount;
        for (int i = sorted.length - 1; i >= 0; i--) {
            long d = sorted[i];
            int idx = indexOf(denominations, d);
            count[idx] = remaining / d;
            remaining %= d;
        }
        return remaining == 0 ? count : null;
    }

    /** Exact minimum coin count for every amount 0..maxAmount by dynamic programming; Long.MAX_VALUE marks impossible. O(maxAmount * k). */
    static long[] optimalTable(long[] denominations, long maxAmount) {
        if (maxAmount < 0) throw new IllegalArgumentException("negative amount");
        long[] best = new long[(int) maxAmount + 1];
        Arrays.fill(best, Long.MAX_VALUE);
        best[0] = 0;
        for (int a = 1; a <= maxAmount; a++) {
            for (long d : denominations) {
                if (d <= a && best[(int) (a - d)] != Long.MAX_VALUE) best[a] = Math.min(best[a], best[(int) (a - d)] + 1);
            }
        }
        return best;
    }

    /** Exact minimum coin count for one amount: the oracle greedy is tested against. */
    static long optimalCount(long[] denominations, long amount) {
        return optimalTable(denominations, amount)[(int) amount];
    }

    static long total(long[] counts) { long t = 0; for (long c : counts) t += c; return t; }

    /**
     * Kozen-Zaks test: a denomination system is canonical (greedy is optimal for every amount) iff greedy is optimal
     * for every amount below d[k-1] + d[k], the sum of the two largest denominations. Needs a 1-coin to make every amount.
     */
    static boolean isCanonical(long[] denominations) {
        long[] sorted = denominations.clone();
        Arrays.sort(sorted);
        if (sorted[0] != 1) return false;
        long limit = sorted.length >= 2 ? sorted[sorted.length - 1] + sorted[sorted.length - 2] : sorted[0];
        long[] optimal = optimalTable(denominations, limit);
        for (long amount = 1; amount < limit; amount++) {
            long[] g = greedy(denominations, amount);
            if (g == null || total(g) != optimal[(int) amount]) return false;
        }
        return true;
    }

    private static int indexOf(long[] a, long v) { for (int i = 0; i < a.length; i++) if (a[i] == v) return i; return -1; }
}

public final class Main {
    public static void main(String[] args) {
        Currency usd = Currency.USD;

        System.out.println("== Interval scheduling: settlement windows that need an exclusive lane ==");
        List<Interval> windows = List.of(
            new Interval("ACH-batch", 0, 6), new Interval("card-settle", 1, 4), new Interval("wire-cutoff", 3, 5),
            new Interval("fx-fixing", 3, 8), new Interval("payout-run", 4, 7), new Interval("recon", 5, 9),
            new Interval("eod-close", 6, 10), new Interval("reporting", 8, 11));
        List<Interval> best = IntervalScheduling.maxNonOverlapping(windows);
        System.out.println("earliest-end-first picks " + best.size() + ": " + best.stream().map(Interval::name).toList());
        List<Interval> worse = IntervalScheduling.shortestFirst(windows);
        System.out.println("shortest-first picks     " + worse.size() + ": " + worse.stream().map(Interval::name).toList());
        System.out.println("lanes needed to run all " + windows.size() + " windows: " + IntervalScheduling.minResources(windows));

        System.out.println();
        System.out.println("== Huffman: a prefix code for ISO 8583 response codes by frequency ==");
        Map<Integer, Long> freq = new TreeMap<>(Map.of(0, 850_000L, 5, 60_000L, 51, 45_000L, 14, 20_000L, 54, 15_000L, 91, 7_000L, 96, 3_000L));
        Huffman.Node tree = Huffman.build(freq);
        Map<Integer, String> codes = Huffman.codes(tree);
        for (Map.Entry<Integer, Long> e : freq.entrySet())
            System.out.printf("  code %2d  freq %,8d  bits %s%n", e.getKey(), e.getValue(), codes.get(e.getKey()));
        long huff = Huffman.encodedBits(freq, codes);
        long total = freq.values().stream().mapToLong(Long::longValue).sum();
        long fixed = total * 3;   // 7 symbols need 3 bits each in a fixed-width code
        System.out.printf("fixed 3-bit code: %,d bits; Huffman: %,d bits (%.1f%% of fixed); prefix-free: %b%n", fixed, huff, 100.0 * huff / fixed, Huffman.isPrefixFree(codes));
        int[] msg = {0, 0, 0, 5, 0, 51, 0, 0, 14, 0};
        String bits = Huffman.encode(msg, codes);
        System.out.println("encode " + Arrays.toString(msg) + " -> " + bits + " -> decode " + Arrays.toString(Huffman.decode(bits, tree)));

        System.out.println();
        System.out.println("== Change-making: greedy is optimal for some denomination sets and wrong for others ==");
        long[] usdCoins = {1, 5, 10, 25, 100, 500, 1000, 2000, 5000, 10000};
        long[] odd = {1, 3, 4};
        long[] oldUk = {1, 3, 6, 12, 24, 30, 60, 240};   // pre-decimal pence: penny, threepence, sixpence, shilling, florin, half-crown, crown, pound
        System.out.printf("USD-like %s canonical? %b%n", Arrays.toString(usdCoins), ChangeMaking.isCanonical(usdCoins));
        System.out.printf("{1,3,4}  canonical? %b: amount 6 -> greedy %d coins, optimal %d%n",
                ChangeMaking.isCanonical(odd), ChangeMaking.total(ChangeMaking.greedy(odd, 6)), ChangeMaking.optimalCount(odd, 6));
        System.out.printf("pre-1971 UK pence canonical? %b: amount 48 -> greedy %d coins (30+12+6), optimal %d (24+24)%n",
                ChangeMaking.isCanonical(oldUk), ChangeMaking.total(ChangeMaking.greedy(oldUk, 48)), ChangeMaking.optimalCount(oldUk, 48));
        long[] payout = ChangeMaking.greedy(usdCoins, 18_741);
        System.out.println("pay " + Money.ofMinor(18_741, usd) + " greedily: " + ChangeMaking.total(payout) + " pieces " + describe(usdCoins, payout));
    }

    static String describe(long[] denoms, long[] counts) {
        StringBuilder sb = new StringBuilder("{");
        for (int i = denoms.length - 1; i >= 0; i--) if (counts[i] > 0) sb.append(counts[i]).append("x").append(denoms[i]).append(' ');
        return sb.toString().trim() + "}";
    }
}
