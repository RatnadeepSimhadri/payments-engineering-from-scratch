// Tests for phases/02-algorithms/04-recursion-and-backtracking. Run: javac -d .out *.java && java -cp .out MainTest
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

    static boolean throwsIAE(Runnable r) {
        try { r.run(); return false; } catch (IllegalArgumentException e) { return true; }
    }

    static long sum(List<Money> ms) { long s = 0; for (Money m : ms) s += m.minorUnits(); return s; }

    public static void main(String[] args) {
        Currency usd = Currency.USD;

        // permutations
        List<List<String>> perms = Enumerate.permutations(List.of("A", "B", "C"));
        check("3! permutations", perms.size() == 6);
        check("permutations are distinct", new HashSet<>(perms).size() == 6);
        check("first permutation is identity order", perms.get(0).equals(List.of("A", "B", "C")));
        check("last permutation is reverse", perms.get(5).equals(List.of("C", "B", "A")));
        check("permutations of empty list: one empty permutation", Enumerate.permutations(List.of()).size() == 1);
        check("5! = 120", Enumerate.permutations(List.of(1, 2, 3, 4, 5)).size() == 120);

        // subsets
        List<List<String>> subs = Enumerate.subsets(List.of("A", "B", "C"));
        check("2^3 subsets", subs.size() == 8);
        check("subsets are distinct", new HashSet<>(subs).size() == 8);
        check("empty set first", subs.get(0).isEmpty());
        check("full set last", subs.get(7).equals(List.of("A", "B", "C")));
        check("subsets preserve input order within each", subs.stream().allMatch(s -> { for (int i = 1; i < s.size(); i++) if (s.get(i - 1).compareTo(s.get(i)) >= 0) return false; return true; }));
        check("2^10 subsets", Enumerate.subsets(List.of(0, 1, 2, 3, 4, 5, 6, 7, 8, 9)).size() == 1024);

        // combinations
        check("C(4,2) = 6", Enumerate.combinations(List.of("A", "B", "C", "D"), 2).size() == 6);
        check("C(5,0) = 1", Enumerate.combinations(List.of(1, 2, 3, 4, 5), 0).size() == 1);
        check("C(5,5) = 1", Enumerate.combinations(List.of(1, 2, 3, 4, 5), 5).size() == 1);
        check("C(10,3) = 120", Enumerate.combinations(List.of(0, 1, 2, 3, 4, 5, 6, 7, 8, 9), 3).size() == 120);
        check("combinations reject k > n", throwsIAE(() -> Enumerate.combinations(List.of(1, 2), 3)));

        // settlement allocations
        List<Money> receivables = List.of(Money.ofMinor(4_500, usd), Money.ofMinor(7_500, usd), Money.ofMinor(2_000, usd),
                Money.ofMinor(12_000, usd), Money.ofMinor(5_500, usd), Money.ofMinor(1_000, usd), Money.ofMinor(3_000, usd));
        Money target = Money.ofMinor(12_000, usd);
        Counter cNo = new Counter(), cYes = new Counter(), cIt = new Counter();
        List<List<Money>> noPrune = SettlementAllocator.allocations(receivables, target, false, cNo);
        List<List<Money>> pruned = SettlementAllocator.allocations(receivables, target, true, cYes);
        List<List<Money>> iter = SettlementAllocator.allocationsIterative(receivables, target, cIt);
        check("every allocation sums to the target", pruned.stream().allMatch(a -> sum(a) == 12_000));
        check("allocations are distinct", new HashSet<>(pruned).size() == pruned.size());
        check("expected number of allocations", pruned.size() == 3);
        check("pruning does not change the answer", noPrune.equals(pruned));
        check("iterative version matches the recursive one, same order", iter.equals(pruned));
        check("pruning visits fewer nodes", cYes.visited < cNo.visited);
        check("unpruned search visits a node per subset prefix (2^(n+1) - 1 bound)", cNo.visited <= (1L << 8) - 1);
        check("iterative visits the same number of nodes as pruned recursion", cIt.visited == cYes.visited);
        // the single-item allocation [120.00] must be among them
        check("single receivable equal to target is an allocation", pruned.contains(List.of(Money.ofMinor(12_000, usd))));
        // an impossible target
        check("no allocation for an unreachable target", SettlementAllocator.allocations(receivables, Money.ofMinor(1, usd), true, new Counter()).isEmpty());
        check("target zero has exactly the empty allocation", SettlementAllocator.allocations(receivables, Money.ofMinor(0, usd), true, new Counter()).equals(List.of(List.of())));
        check("rejects mixed currencies", throwsIAE(() -> SettlementAllocator.allocations(List.of(Money.ofMinor(1, new Currency("EUR", 2))), target, true, new Counter())));
        check("rejects non-positive receivables", throwsIAE(() -> SettlementAllocator.allocations(List.of(Money.ofMinor(0, usd)), target, true, new Counter())));

        // pruning at scale: same answers, far fewer nodes
        List<Money> many = new ArrayList<>();
        for (int i = 1; i <= 20; i++) many.add(Money.ofMinor(100L * i * (i % 5 + 1), usd));
        Counter big1 = new Counter(), big2 = new Counter();
        List<List<Money>> r1 = SettlementAllocator.allocations(many, Money.ofMinor(30_000, usd), false, big1);
        List<List<Money>> r2 = SettlementAllocator.allocations(many, Money.ofMinor(30_000, usd), true, big2);
        check("20 receivables: pruned and unpruned agree", r1.equals(r2));
        check("20 receivables: pruning cuts nodes by more than 10x", big2.visited * 10 < big1.visited);

        // n-queens
        Counter q4 = new Counter(), q6 = new Counter(), q8 = new Counter();
        check("4-queens has 2 solutions", NQueens.countSolutions(4, q4) == 2);
        check("6-queens has 4 solutions", NQueens.countSolutions(6, q6) == 4);
        check("8-queens has 92 solutions", NQueens.countSolutions(8, q8) == 92);
        check("8-queens visits far fewer than 8^8 nodes", q8.visited < 20_000);
        check("1-queen has 1 solution", NQueens.countSolutions(1, new Counter()) == 1);
        check("3-queens has no solution", NQueens.countSolutions(3, new Counter()) == 0);

        // depth
        check("recursive and iterative sums agree", Depth.sumRecursive(1_000) == Depth.sumIterative(1_000) && Depth.sumIterative(1_000) == 500_500L);
        boolean overflowed = false;
        try { Depth.sumRecursive(50_000_000L); } catch (StackOverflowError e) { overflowed = true; }
        check("deep recursion overflows the stack; the failure the lesson warns about", overflowed);
        check("iterative sum handles the same depth", Depth.sumIterative(50_000_000L) == 50_000_000L * 50_000_001L / 2);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
