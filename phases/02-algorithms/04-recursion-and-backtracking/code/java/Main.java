// Lesson: phases/02-algorithms/04-recursion-and-backtracking/docs/en.md
// Recursion and Backtracking
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Knuth, TAOCP Vol. 4A, Section 7.2.1 (generating all n-tuples, permutations, combinations);
// Golomb & Baumert, "Backtrack Programming" (JACM 1965). Java 21, standard library only.

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Deque;
import java.util.List;
import java.util.Objects;
import java.util.function.Consumer;

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

    Money plus(Money o) { requireSame(o); return new Money(Math.addExact(minorUnits, o.minorUnits), currency); }

    private void requireSame(Money o) {
        if (!currency.equals(o.currency)) throw new IllegalArgumentException("cannot combine " + currency.code() + " with " + o.currency.code());
    }

    @Override public int compareTo(Money other) { requireSame(other); return Long.compare(minorUnits, other.minorUnits); }

    @Override public String toString() {
        long scale = 1;
        for (int i = 0; i < currency.exponent(); i++) scale *= 10;
        long whole = Math.abs(minorUnits) / scale, frac = Math.abs(minorUnits) % scale;
        String sign = minorUnits < 0 ? "-" : "";
        if (currency.exponent() == 0) return sign + whole + " " + currency.code();
        return String.format("%s%d.%0" + currency.exponent() + "d %s", sign, whole, frac, currency.code());
    }
}

/** Counts how many nodes of the search tree were visited: the honest measure of pruning. */
final class Counter {
    long visited = 0;
}

/** Exhaustive generation: every permutation and every subset, by recursion that undoes its own choices. */
final class Enumerate {
    private Enumerate() {}

    /** All n! orderings of the elements, in lexicographic order of positions. */
    static <T> List<List<T>> permutations(List<T> items) {
        List<List<T>> out = new ArrayList<>();
        boolean[] used = new boolean[items.size()];
        permute(items, used, new ArrayList<>(), out);
        return out;
    }

    private static <T> void permute(List<T> items, boolean[] used, List<T> current, List<List<T>> out) {
        if (current.size() == items.size()) { out.add(new ArrayList<>(current)); return; }   // leaf: copy the path
        for (int i = 0; i < items.size(); i++) {
            if (used[i]) continue;
            used[i] = true; current.add(items.get(i));          // choose
            permute(items, used, current, out);                 // explore
            current.remove(current.size() - 1); used[i] = false; // un-choose: the backtrack step
        }
    }

    /** All 2^n subsets, each as a list preserving the input order; the empty set first. */
    static <T> List<List<T>> subsets(List<T> items) {
        List<List<T>> out = new ArrayList<>();
        subsetsFrom(items, 0, new ArrayList<>(), out);
        return out;
    }

    private static <T> void subsetsFrom(List<T> items, int index, List<T> current, List<List<T>> out) {
        if (index == items.size()) { out.add(new ArrayList<>(current)); return; }
        subsetsFrom(items, index + 1, current, out);            // branch 1: leave items[index] out
        current.add(items.get(index));
        subsetsFrom(items, index + 1, current, out);            // branch 2: take it
        current.remove(current.size() - 1);
    }

    /** All C(n, k) combinations of size k, in lexicographic index order. */
    static <T> List<List<T>> combinations(List<T> items, int k) {
        if (k < 0 || k > items.size()) throw new IllegalArgumentException("k out of range: " + k);
        List<List<T>> out = new ArrayList<>();
        combine(items, k, 0, new ArrayList<>(), out);
        return out;
    }

    private static <T> void combine(List<T> items, int k, int start, List<T> current, List<List<T>> out) {
        if (current.size() == k) { out.add(new ArrayList<>(current)); return; }
        // prune: if even taking every remaining item cannot reach k, stop
        for (int i = start; i <= items.size() - (k - current.size()); i++) {
            current.add(items.get(i));
            combine(items, k, i + 1, current, out);
            current.remove(current.size() - 1);
        }
    }
}

/**
 * The payments problem: a settlement of `target` must be covered by a subset of pending receivables.
 * Enumerate every subset whose amounts sum exactly to the target (an exact-cover of the amount).
 * Pruning: sort descending, stop a branch when the running sum exceeds the target, and stop when the
 * remaining items cannot reach it. Still exponential in the worst case; that is the lesson.
 */
final class SettlementAllocator {
    private SettlementAllocator() {}

    static List<List<Money>> allocations(List<Money> receivables, Money target, boolean prune, Counter counter) {
        Currency ccy = target.currency();
        for (Money m : receivables) {
            if (!m.currency().equals(ccy)) throw new IllegalArgumentException("mixed currencies");
            if (m.minorUnits() <= 0) throw new IllegalArgumentException("receivables must be positive");
        }
        List<Money> sorted = new ArrayList<>(receivables);
        sorted.sort((a, b) -> Long.compare(b.minorUnits(), a.minorUnits()));   // largest first: prune earlier
        long[] suffix = new long[sorted.size() + 1];                            // suffix[i] = sum of sorted[i..]
        for (int i = sorted.size() - 1; i >= 0; i--) suffix[i] = suffix[i + 1] + sorted.get(i).minorUnits();
        List<List<Money>> out = new ArrayList<>();
        search(sorted, suffix, 0, 0, target.minorUnits(), new ArrayList<>(), out, prune, counter);
        return out;
    }

    private static void search(List<Money> items, long[] suffix, int index, long sum, long target,
                               List<Money> chosen, List<List<Money>> out, boolean prune, Counter counter) {
        counter.visited++;
        if (sum == target) { out.add(new ArrayList<>(chosen)); return; }     // found one; nothing more can be added (positive amounts)
        if (index == items.size()) return;
        if (prune) {
            if (sum > target) return;                                          // overshot: no positive amount can fix it
            if (sum + suffix[index] < target) return;                          // even taking everything left falls short
        }
        chosen.add(items.get(index));
        search(items, suffix, index + 1, sum + items.get(index).minorUnits(), target, chosen, out, prune, counter);
        chosen.remove(chosen.size() - 1);
        search(items, suffix, index + 1, sum, target, chosen, out, prune, counter);
    }

    /** Same search, no recursion: an explicit stack of frames. Produces the same allocations in the same order. */
    static List<List<Money>> allocationsIterative(List<Money> receivables, Money target, Counter counter) {
        List<Money> sorted = new ArrayList<>(receivables);
        sorted.sort((a, b) -> Long.compare(b.minorUnits(), a.minorUnits()));
        long[] suffix = new long[sorted.size() + 1];
        for (int i = sorted.size() - 1; i >= 0; i--) suffix[i] = suffix[i + 1] + sorted.get(i).minorUnits();
        long t = target.minorUnits();

        record Frame(int index, long sum, List<Money> chosen) {}
        List<List<Money>> out = new ArrayList<>();
        Deque<Frame> stack = new ArrayDeque<>();
        stack.push(new Frame(0, 0, List.of()));
        while (!stack.isEmpty()) {
            Frame f = stack.pop();
            counter.visited++;
            if (f.sum() == t) { out.add(f.chosen()); continue; }
            if (f.index() == sorted.size() || f.sum() > t || f.sum() + suffix[f.index()] < t) continue;
            // push the "skip" branch first so the "take" branch is popped (explored) first, matching the recursion's order
            stack.push(new Frame(f.index() + 1, f.sum(), f.chosen()));
            List<Money> taken = new ArrayList<>(f.chosen());
            taken.add(sorted.get(f.index()));
            stack.push(new Frame(f.index() + 1, f.sum() + sorted.get(f.index()).minorUnits(), taken));
        }
        return out;
    }
}

/** N-Queens: the classic constraint-satisfaction backtrack, with a visit counter to show what pruning buys. */
final class NQueens {
    private NQueens() {}

    /** Number of solutions on an n x n board. */
    static long countSolutions(int n, Counter counter) {
        return place(n, 0, new boolean[n], new boolean[2 * n], new boolean[2 * n], counter);
    }

    private static long place(int n, int row, boolean[] cols, boolean[] diag1, boolean[] diag2, Counter counter) {
        counter.visited++;
        if (row == n) return 1;
        long found = 0;
        for (int col = 0; col < n; col++) {
            int d1 = row + col, d2 = row - col + n;
            if (cols[col] || diag1[d1] || diag2[d2]) continue;      // constraint check before descending: the prune
            cols[col] = diag1[d1] = diag2[d2] = true;
            found += place(n, row + 1, cols, diag1, diag2, counter);
            cols[col] = diag1[d1] = diag2[d2] = false;
        }
        return found;
    }
}

/** Recursion depth is a resource. These show where the JVM stack ends and how to convert when it matters. */
final class Depth {
    private Depth() {}

    static long sumRecursive(long n) { return n == 0 ? 0 : n + sumRecursive(n - 1); }

    static long sumIterative(long n) { long s = 0; for (long i = 1; i <= n; i++) s += i; return s; }

    /** Depth at which the current thread's stack overflows for a trivial frame; measured, not assumed. */
    static int measureMaxDepth() {
        int[] depth = {0};
        try { probe(depth); } catch (StackOverflowError e) { /* expected */ }
        return depth[0];
    }

    private static void probe(int[] depth) { depth[0]++; probe(depth); }
}

public final class Main {
    public static void main(String[] args) {
        Currency usd = Currency.USD;

        System.out.println("== Permutations of [A, B, C] ==");
        Enumerate.permutations(List.of("A", "B", "C")).forEach(p -> System.out.println("  " + p));
        System.out.println("== Subsets of [A, B, C] ==");
        System.out.println("  " + Enumerate.subsets(List.of("A", "B", "C")));
        System.out.println("== Combinations C(4, 2) of [A, B, C, D] ==");
        System.out.println("  " + Enumerate.combinations(List.of("A", "B", "C", "D"), 2));

        System.out.println();
        System.out.println("== Settlement allocation: which receivables exactly cover 120.00 USD? ==");
        List<Money> receivables = List.of(Money.ofMinor(4_500, usd), Money.ofMinor(7_500, usd), Money.ofMinor(2_000, usd),
                Money.ofMinor(12_000, usd), Money.ofMinor(5_500, usd), Money.ofMinor(1_000, usd), Money.ofMinor(3_000, usd));
        Money target = Money.ofMinor(12_000, usd);
        Counter noPrune = new Counter(), withPrune = new Counter(), iterative = new Counter();
        List<List<Money>> a1 = SettlementAllocator.allocations(receivables, target, false, noPrune);
        List<List<Money>> a2 = SettlementAllocator.allocations(receivables, target, true, withPrune);
        List<List<Money>> a3 = SettlementAllocator.allocationsIterative(receivables, target, iterative);
        System.out.println("receivables: " + receivables);
        for (List<Money> alloc : a2) System.out.println("  " + alloc);
        System.out.printf("%d allocations; nodes visited: no pruning %d, pruning %d, iterative %d; same results: %b%n",
                a2.size(), noPrune.visited, withPrune.visited, iterative.visited, a1.equals(a2) && a2.equals(a3));

        System.out.println();
        System.out.println("== Pruning at scale: 24 receivables, one target ==");
        List<Money> many = new ArrayList<>();
        for (int i = 1; i <= 24; i++) many.add(Money.ofMinor(100L * i * (i % 5 + 1), usd));   // 24 distinct amounts
        Money big = Money.ofMinor(50_000, usd);
        Counter c1 = new Counter(), c2 = new Counter();
        int n1 = SettlementAllocator.allocations(many, big, false, c1).size();
        int n2 = SettlementAllocator.allocations(many, big, true, c2).size();
        System.out.printf("%d allocations found both ways; nodes visited without pruning: %,d (2^25 - 1 = %,d), with pruning: %,d%n",
                n2, c1.visited, (1L << 25) - 1, c2.visited);
        if (n1 != n2) throw new AssertionError("pruning changed the answer");

        System.out.println();
        System.out.println("== N-Queens: constraint checks before descending ==");
        for (int n : new int[] {4, 6, 8}) {
            Counter c = new Counter();
            long solutions = NQueens.countSolutions(n, c);
            System.out.printf("  n=%d: %d solutions, %,d nodes visited (of %,d leaves in the unpruned tree)%n", n, solutions, c.visited, (long) Math.pow(n, n));
        }

        System.out.println();
        System.out.println("== Recursion depth is finite ==");
        int maxDepth = Depth.measureMaxDepth();
        System.out.println("sumRecursive(1_000) = " + Depth.sumRecursive(1_000) + ", sumIterative(10_000_000) = " + Depth.sumIterative(10_000_000));
        System.out.println("this thread overflowed at a recursion depth of about " + maxDepth + " trivial frames (JVM default stack; -Xss changes it)");
        System.out.println("a recursive sum over a day's 2,000,000 ledger lines would not survive; the explicit-stack version does not care");
    }
}
