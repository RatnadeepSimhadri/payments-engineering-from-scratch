// Tests for phases/02-algorithms/06-dynamic-programming. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;

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
        Currency USD = Currency.USD;
        int[] us = {1, 5, 10, 25};
        int[] odd = {1, 3, 4};

        // min coins: table, reconstruction, greedy counterexample
        ChangeResult sixtyThree = CoinChange.minCoinsTable(us, 63);
        check("63c in US coins is 6 coins", sixtyThree.coinCount() == 6);
        check("63c reconstruction = 25+25+10+1+1+1", sixtyThree.coins(us).equals(List.of(25, 25, 10, 1, 1, 1)));
        check("reconstruction sums to amount", sum(sixtyThree.coins(us)) == 63);
        ChangeResult six = CoinChange.minCoinsTable(odd, 6);
        check("{1,3,4} for 6 is 2 coins", six.coinCount() == 2);
        check("{1,3,4} for 6 uses 3+3", six.coins(odd).equals(List.of(3, 3)));
        check("greedy gets {1,3,4} for 6 wrong", CoinChange.greedyCoins(odd, 6) == 3);
        check("greedy agrees on canonical US coins", CoinChange.greedyCoins(us, 63) == 6);
        check("amount 0 needs 0 coins", CoinChange.minCoinsTable(us, 0).coinCount() == 0);
        check("unreachable amount is UNREACHABLE", CoinChange.minCoinsTable(new int[]{2}, 3).coinCount() == ChangeResult.UNREACHABLE);
        check("unreachable reports reachable()==false", !CoinChange.minCoinsTable(new int[]{2}, 3).reachable());
        check("denomination order does not change the count",
                CoinChange.minCoinsTable(new int[]{25, 1, 10, 5}, 63).coinCount() == 6);
        checkThrows("negative amount rejected", IllegalArgumentException.class, () -> CoinChange.minCoinsTable(us, -1));
        checkThrows("zero denomination rejected", IllegalArgumentException.class, () -> CoinChange.minCoinsTable(new int[]{0, 1}, 5));
        checkThrows("empty denominations rejected", IllegalArgumentException.class, () -> CoinChange.minCoinsTable(new int[]{}, 5));

        // recurrence, memo, and table agree; memo does far less work
        boolean agree = true;
        for (int a = 0; a <= 20; a++) {
            long[] c1 = {0}, c2 = {0};
            int r = CoinChange.minCoinsRecursive(odd, a, c1);
            int m = CoinChange.minCoinsMemo(odd, a, c2);
            int t = CoinChange.minCoinsTable(odd, a).coinCount();
            if (r != m || m != t) agree = false;
        }
        check("recursive == memo == table for 0..20", agree);
        long[] naive = {0}, memo = {0};
        CoinChange.minCoinsRecursive(odd, 20, naive);
        CoinChange.minCoinsMemo(odd, 20, memo);
        check("recursive call count is exponential (20736 for amount 20)", naive[0] == 20736);
        check("memo call count is linear-ish (56 for amount 20)", memo[0] == 56);
        check("memo returns INF-equivalent unreachable", CoinChange.minCoinsMemo(new int[]{2}, 3, new long[1]) == CoinChange.INF);

        // number of ways
        check("ways to make 5 from {1,2,5} = 4", CoinChange.countWays(new int[]{1, 2, 5}, 5) == 4);
        check("ways to make 0 = 1 (the empty bundle)", CoinChange.countWays(us, 0) == 1);
        check("ways for unreachable = 0", CoinChange.countWays(new int[]{2}, 3) == 0);
        check("ways to make 1.00 USD from US coins = 242", CoinChange.countWays(us, 100) == 242);
        check("ways is independent of denomination order", CoinChange.countWays(new int[]{5, 2, 1}, 5) == 4);

        // knapsack: payout batching
        List<Payout> payouts = List.of(
                new Payout("P-A", Money.ofMinor(6000, USD), 100),
                new Payout("P-B", Money.ofMinor(5000, USD), 90),
                new Payout("P-C", Money.ofMinor(5000, USD), 90),
                new Payout("P-D", Money.ofMinor(3000, USD), 10),
                new Payout("P-E", Money.ofMinor(1000, USD), 5),
                new Payout("P-F", Money.ofMinor(12000, USD), 999));
        Money limit = Money.ofMinor(10000, USD);
        Batch best = Knapsack.bestBatch(payouts, limit);
        check("knapsack priority is 180", best.priority() == 180);
        check("knapsack chooses B and C", Main.ids(best.chosen()).equals(List.of("P-B", "P-C")));
        check("knapsack total within limit", best.total().compareTo(limit) <= 0);
        check("reconstructed priority equals table value", best.chosen().stream().mapToLong(Payout::priority).sum() == best.priority());
        check("reconstructed total equals sum of chosen", best.total().minorUnits() == 10000);
        check("oversize payout never chosen", best.chosen().stream().noneMatch(p -> p.id().equals("P-F")));
        Batch greedy = Knapsack.greedyBatch(payouts, limit);
        check("greedy by priority is worse (115)", greedy.priority() == 115 && greedy.priority() < best.priority());
        check("empty payouts give empty batch", Knapsack.bestBatch(List.of(), limit).chosen().isEmpty());
        check("zero limit chooses nothing", Knapsack.bestBatch(payouts, Money.ofMinor(0, USD)).priority() == 0);
        check("exact fit of a single item", Knapsack.bestBatch(List.of(new Payout("X", Money.ofMinor(700, USD), 7)), Money.ofMinor(700, USD)).priority() == 7);
        checkThrows("currency mismatch rejected", CurrencyMismatchException.class,
                () -> Knapsack.bestBatch(List.of(new Payout("E", Money.ofMinor(100, Currency.EUR), 1)), limit));
        checkThrows("non-positive amount rejected", IllegalArgumentException.class,
                () -> Knapsack.bestBatch(List.of(new Payout("Z", Money.ofMinor(0, USD), 1)), limit));

        // LCS
        List<String> x = List.of("A", "B", "C", "B", "D", "A", "B");
        List<String> y = List.of("B", "D", "C", "A", "B", "A");
        check("LCS length of the CLRS example is 4", Lcs.length(x, y) == 4);
        Alignment al = Lcs.align(x, y);
        check("LCS reconstruction has length 4", al.common().size() == 4);
        check("LCS common is a subsequence of both", isSubsequence(al.common(), x) && isSubsequence(al.common(), y));
        check("left-only + common = |a|", al.leftOnly().size() + al.common().size() == x.size());
        check("right-only + common = |b|", al.rightOnly().size() + al.common().size() == y.size());
        check("LCS of identical lists is the list", Lcs.align(x, x).common().equals(x) && Lcs.align(x, x).leftOnly().isEmpty());
        check("LCS of disjoint lists is empty", Lcs.length(List.of("A", "B"), List.of("C", "D")) == 0);
        check("LCS with an empty list is 0", Lcs.length(List.of(), y) == 0);
        List<String> bank = List.of("DEP 500.00", "CARD 19.99", "CARD 42.10", "FEE 2.50", "CARD 7.00");
        List<String> ledger = List.of("DEP 500.00", "CARD 42.10", "CARD 19.99", "CARD 7.00", "REFUND 19.99");
        Alignment st = Lcs.align(bank, ledger);
        check("statement alignment matches 3 lines in order", st.common().equals(List.of("DEP 500.00", "CARD 19.99", "CARD 7.00")));
        check("statement alignment: bank-only lines", st.leftOnly().equals(List.of("CARD 42.10", "FEE 2.50")));
        check("statement alignment: ledger-only lines", st.rightOnly().equals(List.of("CARD 42.10", "REFUND 19.99")));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }

    static int sum(List<Integer> xs) {
        int s = 0;
        for (int v : xs) s += v;
        return s;
    }

    static boolean isSubsequence(List<String> sub, List<String> seq) {
        int i = 0;
        for (String s : seq) if (i < sub.size() && sub.get(i).equals(s)) i++;
        return i == sub.size();
    }
}
