// Lesson: phases/02-algorithms/06-dynamic-programming/docs/en.md
// Dynamic Programming: From Recurrence to Table
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Bellman, "Dynamic Programming" (1957); CLRS ch. 15 (rod cutting, LCS); Kellerer, Pferschy,
// Pisinger, "Knapsack Problems" (2004). Money design copied from phases/10-money-and-ledgers/01-representing-money.
// Java 21, standard library only.

import java.util.ArrayList;
import java.util.Arrays;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;

/** ISO 4217 currency: code and minor-unit exponent. Trimmed copy of the Phase 10 Lesson 01 type. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency EUR = new Currency("EUR", 2);
    static final Currency INR = new Currency("INR", 2);

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

final class CurrencyMismatchException extends IllegalArgumentException {
    CurrencyMismatchException(Currency a, Currency b) {
        super("cannot combine " + a.code() + " with " + b.code());
    }
}

/** Exact amount of one currency as a signed count of minor units. Never a float. */
record Money(long minorUnits, Currency currency) implements Comparable<Money> {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
    }

    Money plus(Money other) {
        requireSameCurrency(other);
        return new Money(Math.addExact(minorUnits, other.minorUnits), currency);
    }

    @Override
    public int compareTo(Money other) {
        requireSameCurrency(other);
        return Long.compare(minorUnits, other.minorUnits);
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

/** Result of the min-coins table: how many instruments, and how many of each denomination. */
record ChangeResult(int coinCount, int[] used) {
    static final int UNREACHABLE = -1;

    boolean reachable() { return coinCount != UNREACHABLE; }

    /** Expand the counts back into a sorted list of face values, largest first, for display. */
    List<Integer> coins(int[] denominations) {
        List<Integer> out = new ArrayList<>();
        for (int i = 0; i < denominations.length; i++) {
            for (int k = 0; k < used[i]; k++) out.add(denominations[i]);
        }
        out.sort((a, b) -> Integer.compare(b, a));
        return out;
    }
}

/**
 * Coin change in three forms: the bare recurrence, the memoised recurrence, and the bottom-up table.
 * Denominations and amounts are integer minor units (cents, paise); the table is indexed by amount.
 */
final class CoinChange {
    static final int INF = Integer.MAX_VALUE / 2; // half of MAX so INF + 1 cannot overflow

    private static void validate(int[] denominations, int amount) {
        if (amount < 0) throw new IllegalArgumentException("amount must be non-negative: " + amount);
        if (denominations.length == 0) throw new IllegalArgumentException("need at least one denomination");
        for (int d : denominations) if (d <= 0) throw new IllegalArgumentException("denominations must be positive: " + d);
    }

    /** The recurrence as written: f(0) = 0, f(a) = 1 + min over d <= a of f(a - d). Exponential. */
    static int minCoinsRecursive(int[] denominations, int amount, long[] calls) {
        validate(denominations, amount);
        return recurse(denominations, amount, calls);
    }

    private static int recurse(int[] denominations, int amount, long[] calls) {
        calls[0]++;
        if (amount == 0) return 0;
        int best = INF;
        for (int d : denominations) {
            if (d <= amount) {
                int sub = recurse(denominations, amount - d, calls);
                if (sub != INF) best = Math.min(best, sub + 1);
            }
        }
        return best;
    }

    /** Same recurrence, but every subproblem is solved once. O(amount * denominations). */
    static int minCoinsMemo(int[] denominations, int amount, long[] calls) {
        validate(denominations, amount);
        Map<Integer, Integer> memo = new HashMap<>();
        return memoised(denominations, amount, memo, calls);
    }

    private static int memoised(int[] denominations, int amount, Map<Integer, Integer> memo, long[] calls) {
        calls[0]++;
        if (amount == 0) return 0;
        Integer known = memo.get(amount);
        if (known != null) return known;
        int best = INF;
        for (int d : denominations) {
            if (d <= amount) {
                int sub = memoised(denominations, amount - d, memo, calls);
                if (sub != INF) best = Math.min(best, sub + 1);
            }
        }
        memo.put(amount, best);
        return best;
    }

    /**
     * Bottom-up table with a choice array for reconstruction. best[a] is the fewest coins for amount a;
     * choice[a] is the index of the denomination that achieved it (ties go to the lowest index).
     */
    static ChangeResult minCoinsTable(int[] denominations, int amount) {
        validate(denominations, amount);
        int[] best = new int[amount + 1];
        int[] choice = new int[amount + 1];
        Arrays.fill(best, INF);
        Arrays.fill(choice, -1);
        best[0] = 0;
        for (int a = 1; a <= amount; a++) {
            for (int i = 0; i < denominations.length; i++) {
                int d = denominations[i];
                if (d <= a && best[a - d] != INF && best[a - d] + 1 < best[a]) {
                    best[a] = best[a - d] + 1;
                    choice[a] = i;
                }
            }
        }
        int[] used = new int[denominations.length];
        if (best[amount] == INF) return new ChangeResult(ChangeResult.UNREACHABLE, used);
        for (int a = amount; a > 0; a -= denominations[choice[a]]) used[choice[a]]++;
        return new ChangeResult(best[amount], used);
    }

    /**
     * Number of distinct multisets of denominations that sum to amount. Denominations are the OUTER
     * loop so that {1,2} and {2,1} are counted once; swapping the loops counts ordered sequences.
     */
    static long countWays(int[] denominations, int amount) {
        validate(denominations, amount);
        long[] ways = new long[amount + 1];
        ways[0] = 1;
        for (int d : denominations) {
            for (int a = d; a <= amount; a++) ways[a] = Math.addExact(ways[a], ways[a - d]);
        }
        return ways[amount];
    }

    /** Largest-first greedy, for comparison. Correct for canonical coin systems, wrong in general. */
    static int greedyCoins(int[] denominations, int amount) {
        validate(denominations, amount);
        int[] sorted = denominations.clone();
        Arrays.sort(sorted);
        int count = 0;
        for (int i = sorted.length - 1; i >= 0 && amount > 0; i--) {
            count += amount / sorted[i];
            amount %= sorted[i];
        }
        return amount == 0 ? count : ChangeResult.UNREACHABLE;
    }
}

/** A pending payout: an amount (the knapsack weight) and a priority score (the knapsack value). */
record Payout(String id, Money amount, long priority) {}

/** The chosen batch: which payouts, their total, and the priority achieved. */
record Batch(List<Payout> chosen, Money total, long priority) {}

/**
 * 0/1 knapsack: choose a subset of payouts whose amounts fit under the settlement limit and whose
 * priority sum is maximal. Weight is in minor units, so the table has (limit + 1) columns: this is
 * pseudo-polynomial, O(n * limit), and the limit must be small enough to allocate the table.
 */
final class Knapsack {
    static Batch bestBatch(List<Payout> payouts, Money limit) {
        if (limit.minorUnits() < 0) throw new IllegalArgumentException("limit must be non-negative");
        for (Payout p : payouts) {
            if (!p.amount().currency().equals(limit.currency())) throw new CurrencyMismatchException(limit.currency(), p.amount().currency());
            if (p.amount().minorUnits() <= 0) throw new IllegalArgumentException("payout amounts must be positive: " + p.id());
            if (p.priority() < 0) throw new IllegalArgumentException("priority must be non-negative: " + p.id());
        }
        int n = payouts.size();
        int w = Math.toIntExact(limit.minorUnits());
        long[][] best = new long[n + 1][w + 1]; // best[i][c] = max priority using the first i payouts under capacity c
        for (int i = 1; i <= n; i++) {
            Payout p = payouts.get(i - 1);
            int weight = Math.toIntExact(p.amount().minorUnits());
            for (int c = 0; c <= w; c++) {
                long skip = best[i - 1][c];
                long take = weight <= c ? best[i - 1][c - weight] + p.priority() : -1;
                best[i][c] = Math.max(skip, take);
            }
        }
        // Reconstruct by walking the table backwards: a cell that differs from the row above took its item.
        List<Payout> chosen = new ArrayList<>();
        Money total = Money.ofMinor(0, limit.currency());
        int c = w;
        for (int i = n; i >= 1; i--) {
            if (best[i][c] != best[i - 1][c]) {
                Payout p = payouts.get(i - 1);
                chosen.add(0, p);
                total = total.plus(p.amount());
                c -= Math.toIntExact(p.amount().minorUnits());
            }
        }
        return new Batch(chosen, total, best[n][w]);
    }

    /** Highest-priority-first greedy, for comparison. Fast, and wrong whenever two smaller items beat one larger one. */
    static Batch greedyBatch(List<Payout> payouts, Money limit) {
        List<Payout> order = new ArrayList<>(payouts);
        order.sort((a, b) -> Long.compare(b.priority(), a.priority()));
        List<Payout> chosen = new ArrayList<>();
        Money total = Money.ofMinor(0, limit.currency());
        long priority = 0;
        for (Payout p : order) {
            if (total.plus(p.amount()).compareTo(limit) <= 0) {
                chosen.add(p);
                total = total.plus(p.amount());
                priority += p.priority();
            }
        }
        return new Batch(chosen, total, priority);
    }
}

/** Which lines two statements share in order, and which are left over on each side. */
record Alignment(List<String> common, List<String> leftOnly, List<String> rightOnly) {}

/** Longest common subsequence: the preview of statement reconciliation (Phase 12). */
final class Lcs {
    /** The table alone: length[i][j] = LCS of a[0..i) and b[0..j). O(|a| * |b|) time and space. */
    static int[][] table(List<String> a, List<String> b) {
        int[][] len = new int[a.size() + 1][b.size() + 1];
        for (int i = 1; i <= a.size(); i++) {
            for (int j = 1; j <= b.size(); j++) {
                if (a.get(i - 1).equals(b.get(j - 1))) len[i][j] = len[i - 1][j - 1] + 1;
                else len[i][j] = Math.max(len[i - 1][j], len[i][j - 1]);
            }
        }
        return len;
    }

    static int length(List<String> a, List<String> b) {
        return table(a, b)[a.size()][b.size()];
    }

    /** Walk the table back from the corner. Ties prefer moving up (consuming from a) so the result is deterministic. */
    static Alignment align(List<String> a, List<String> b) {
        int[][] len = table(a, b);
        List<String> common = new ArrayList<>(), leftOnly = new ArrayList<>(), rightOnly = new ArrayList<>();
        int i = a.size(), j = b.size();
        while (i > 0 && j > 0) {
            if (a.get(i - 1).equals(b.get(j - 1))) {
                common.add(0, a.get(i - 1));
                i--; j--;
            } else if (len[i - 1][j] >= len[i][j - 1]) {
                leftOnly.add(0, a.get(i - 1));
                i--;
            } else {
                rightOnly.add(0, b.get(j - 1));
                j--;
            }
        }
        while (i > 0) leftOnly.add(0, a.get(--i));
        while (j > 0) rightOnly.add(0, b.get(--j));
        return new Alignment(common, leftOnly, rightOnly);
    }
}

public final class Main {
    public static void main(String[] args) {
        System.out.println("== Recurrence -> memo -> table: fewest coins for 20 with {1, 3, 4} ==");
        int[] odd = {1, 3, 4};
        long[] naive = {0}, memo = {0};
        int r1 = CoinChange.minCoinsRecursive(odd, 20, naive);
        int r2 = CoinChange.minCoinsMemo(odd, 20, memo);
        ChangeResult r3 = CoinChange.minCoinsTable(odd, 20);
        System.out.printf("recursive: %d coins after %d calls%n", r1, naive[0]);
        System.out.printf("memoised : %d coins after %d calls%n", r2, memo[0]);
        System.out.printf("table    : %d coins = %s%n", r3.coinCount(), r3.coins(odd));

        System.out.println("\n== Greedy is wrong for non-canonical denominations ==");
        System.out.printf("{1,3,4} for 6: greedy=%d (4+1+1), optimal=%d (3+3)%n",
                CoinChange.greedyCoins(odd, 6), CoinChange.minCoinsTable(odd, 6).coinCount());
        int[] vouchers = {100, 1500, 2500}; // 1.00, 15.00, 25.00 USD gift instruments, in cents
        ChangeResult voucherPlan = CoinChange.minCoinsTable(vouchers, 3000);
        System.out.printf("30.00 USD in {1, 15, 25} vouchers: greedy=%d instruments, table=%d -> %s cents%n",
                CoinChange.greedyCoins(vouchers, 3000), voucherPlan.coinCount(), voucherPlan.coins(vouchers));
        int[] us = {1, 5, 10, 25};
        ChangeResult sixtyThree = CoinChange.minCoinsTable(us, 63);
        System.out.printf("0.63 USD in US coins: %d coins = %s (greedy agrees: %d)%n",
                sixtyThree.coinCount(), sixtyThree.coins(us), CoinChange.greedyCoins(us, 63));
        System.out.printf("unreachable: 0.03 with only {2} -> coinCount=%d%n", CoinChange.minCoinsTable(new int[]{2}, 3).coinCount());

        System.out.println("\n== Number of ways ==");
        System.out.printf("ways to make 5 from {1,2,5} = %d  (5, 2+2+1, 2+1+1+1, 1+1+1+1+1)%n", CoinChange.countWays(new int[]{1, 2, 5}, 5));
        System.out.printf("ways to make 1.00 USD from US coins = %d%n", CoinChange.countWays(us, 100));

        System.out.println("\n== 0/1 knapsack: payout batch under a 100.00 USD settlement limit ==");
        Currency usd = Currency.USD;
        List<Payout> payouts = List.of(
                new Payout("P-A", Money.ofMinor(6000, usd), 100),
                new Payout("P-B", Money.ofMinor(5000, usd), 90),
                new Payout("P-C", Money.ofMinor(5000, usd), 90),
                new Payout("P-D", Money.ofMinor(3000, usd), 10),
                new Payout("P-E", Money.ofMinor(1000, usd), 5),
                new Payout("P-F", Money.ofMinor(12000, usd), 999));
        Money limit = Money.ofMinor(10000, usd);
        Batch greedy = Knapsack.greedyBatch(payouts, limit);
        Batch best = Knapsack.bestBatch(payouts, limit);
        System.out.println("greedy by priority: " + ids(greedy.chosen()) + " total=" + greedy.total() + " priority=" + greedy.priority());
        System.out.println("knapsack table   : " + ids(best.chosen()) + " total=" + best.total() + " priority=" + best.priority());

        System.out.println("\n== LCS: statement lines in order ==");
        List<String> bank = List.of("DEP 500.00", "CARD 19.99", "CARD 42.10", "FEE 2.50", "CARD 7.00");
        List<String> ledger = List.of("DEP 500.00", "CARD 42.10", "CARD 19.99", "CARD 7.00", "REFUND 19.99");
        Alignment al = Lcs.align(bank, ledger);
        System.out.println("bank   : " + bank);
        System.out.println("ledger : " + ledger);
        System.out.println("matched in order (" + al.common().size() + "): " + al.common());
        System.out.println("bank only   : " + al.leftOnly());
        System.out.println("ledger only : " + al.rightOnly());
    }

    static List<String> ids(List<Payout> payouts) {
        List<String> out = new ArrayList<>();
        for (Payout p : payouts) out.add(p.id());
        return out;
    }
}
