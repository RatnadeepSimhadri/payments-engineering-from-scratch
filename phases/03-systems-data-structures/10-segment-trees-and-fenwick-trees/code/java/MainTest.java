// Tests for phases/03-systems-data-structures/10-segment-trees-and-fenwick-trees. Run: javac -d .out *.java && java -cp .out MainTest
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
        long[] values = {12_500, -3_200, 0, 4_150, -900, 7_000, -2_250, 1_000};

        // Fenwick tree: happy path
        FenwickTree fw = FenwickTree.build(values);
        check("fenwick prefix(0)", fw.prefixSum(0) == 12_500);
        check("fenwick prefix(1)", fw.prefixSum(1) == 9_300);
        check("fenwick prefix(7) is the total", fw.prefixSum(7) == 18_300);
        check("fenwick prefix(-1) is zero", fw.prefixSum(-1) == 0);
        check("fenwick rangeSum(2,5)", fw.rangeSum(2, 5) == 10_250);
        check("fenwick rangeSum single element", fw.rangeSum(4, 4) == -900);
        check("fenwick get", fw.get(5) == 7_000);
        fw.add(1, -100);
        check("fenwick add moves prefix at index", fw.prefixSum(1) == 9_200);
        check("fenwick add moves every later prefix", fw.prefixSum(7) == 18_200);
        check("fenwick add leaves earlier prefix alone", fw.prefixSum(0) == 12_500);
        check("fenwick build equals incremental adds", FenwickTree.build(values).rangeSum(0, 7) == incremental(values).rangeSum(0, 7));
        FenwickTree one = new FenwickTree(1);
        one.add(0, 42);
        check("fenwick size 1", one.prefixSum(0) == 42 && one.rangeSum(0, 0) == 42);
        checkThrows("fenwick rejects size 0", IllegalArgumentException.class, () -> new FenwickTree(0));
        checkThrows("fenwick add out of range", IndexOutOfBoundsException.class, () -> fw.add(8, 1));
        checkThrows("fenwick prefix out of range", IndexOutOfBoundsException.class, () -> fw.prefixSum(8));
        checkThrows("fenwick lo > hi", IllegalArgumentException.class, () -> fw.rangeSum(5, 2));
        checkThrows("fenwick overflow is an error, not a wrap", ArithmeticException.class, () -> {
            FenwickTree f = new FenwickTree(2);
            f.add(0, Long.MAX_VALUE);
            f.add(1, 1);
            f.prefixSum(1);
        });

        // Fenwick descent
        FenwickTree vol = FenwickTree.build(new long[]{5_000, 3_000, 8_000, 2_000, 6_000, 1_000});
        check("descent: exact hit on a prefix", vol.firstIndexWithPrefixAtLeast(8_000) == 1);
        check("descent: between prefixes", vol.firstIndexWithPrefixAtLeast(20_000) == 4);
        check("descent: first element", vol.firstIndexWithPrefixAtLeast(1) == 0);
        check("descent: total exactly", vol.firstIndexWithPrefixAtLeast(25_000) == 5);
        check("descent: beyond total is -1", vol.firstIndexWithPrefixAtLeast(25_001) == -1);
        check("descent: target 0 is index 0", vol.firstIndexWithPrefixAtLeast(0) == 0);
        FenwickTree pow2 = FenwickTree.build(new long[]{1, 1, 1, 1, 1, 1, 1, 1});
        check("descent on power-of-two size", pow2.firstIndexWithPrefixAtLeast(8) == 7 && pow2.firstIndexWithPrefixAtLeast(9) == -1);

        // Segment tree (point set)
        SegmentTree st = new SegmentTree(values);
        check("segtree rangeSum whole", st.rangeSum(0, 7) == 18_300);
        check("segtree rangeSum(2,5)", st.rangeSum(2, 5) == 10_250);
        check("segtree rangeSum(0,0)", st.rangeSum(0, 0) == 12_500);
        check("segtree rangeSum(7,7)", st.rangeSum(7, 7) == 1_000);
        st.set(3, 10_000);
        check("segtree set then get", st.get(3) == 10_000);
        check("segtree set updates ancestors", st.rangeSum(2, 5) == 16_100 && st.rangeSum(0, 7) == 24_150);
        SegmentTree odd = new SegmentTree(new long[]{1, 2, 3, 4, 5});     // n not a power of two
        check("segtree non-power-of-two size: full sum", odd.rangeSum(0, 4) == 15);
        check("segtree non-power-of-two size: tail range", odd.rangeSum(3, 4) == 9);
        check("segtree non-power-of-two size: padding is not counted", odd.rangeSum(4, 4) == 5);
        checkThrows("segtree rejects empty", IllegalArgumentException.class, () -> new SegmentTree(new long[0]));
        checkThrows("segtree lo > hi", IllegalArgumentException.class, () -> odd.rangeSum(3, 1));
        checkThrows("segtree out of range", IndexOutOfBoundsException.class, () -> odd.rangeSum(0, 5));

        // Lazy segment tree (range add + range sum)
        LazySegmentTree lazy = new LazySegmentTree(new long[]{0, 0, 0, 0, 0, 0, 0, 0, 0, 0});
        lazy.rangeAdd(2, 6, 10);
        check("lazy rangeAdd then sum inside", lazy.rangeSum(2, 6) == 50);
        check("lazy rangeAdd then sum overlapping", lazy.rangeSum(0, 3) == 20);
        check("lazy rangeAdd leaves outside alone", lazy.rangeSum(7, 9) == 0 && lazy.get(1) == 0);
        lazy.rangeAdd(5, 9, -3);
        check("lazy overlapping adds compose", lazy.get(5) == 7 && lazy.get(6) == 7 && lazy.get(7) == -3 && lazy.get(4) == 10);
        check("lazy total after two adds", lazy.rangeSum(0, 9) == 50 - 15);
        lazy.rangeAdd(0, 9, 1);
        check("lazy whole-range add stops at the root", lazy.rangeSum(0, 9) == 45 && lazy.get(0) == 1 && lazy.get(9) == -2);
        LazySegmentTree single = new LazySegmentTree(new long[]{5});
        single.rangeAdd(0, 0, 7);
        check("lazy size 1", single.get(0) == 12 && single.rangeSum(0, 0) == 12);
        checkThrows("lazy overflow is an error", ArithmeticException.class, () -> {
            LazySegmentTree t = new LazySegmentTree(new long[]{Long.MAX_VALUE, 1});
            t.rangeSum(0, 1);
        });

        // Randomized agreement with a naive array, n not a power of two
        Random rng = new Random(7);
        int n = 131;
        long[] model = new long[n];
        LazySegmentTree lz = new LazySegmentTree(model);
        FenwickTree fz = new FenwickTree(n);
        SegmentTree sz = new SegmentTree(model);
        boolean agree = true;
        for (int op = 0; op < 5_000 && agree; op++) {
            int a = rng.nextInt(n), b = rng.nextInt(n), lo = Math.min(a, b), hi = Math.max(a, b);
            if (rng.nextInt(3) == 0) {
                long delta = rng.nextInt(201) - 100;
                for (int i = lo; i <= hi; i++) { model[i] += delta; fz.add(i, delta); sz.set(i, model[i]); }
                lz.rangeAdd(lo, hi, delta);
            } else {
                long expect = Main.naiveRangeSum(model, lo, hi);
                agree = lz.rangeSum(lo, hi) == expect && fz.rangeSum(lo, hi) == expect && sz.rangeSum(lo, hi) == expect;
            }
        }
        check("all three structures agree with the naive array over 5000 random ops", agree);

        // Balance history: the payments framing
        BalanceHistory h = new BalanceHistory(100_000, 30, Currency.USD);
        check("opening balance on every day", h.balanceOn(0).minorUnits() == 100_000 && h.balanceOn(29).minorUnits() == 100_000);
        h.post(10, -40_000);
        check("posting on day 10 does not move day 9", h.balanceOn(9).minorUnits() == 100_000);
        check("posting on day 10 moves day 10 and after", h.balanceOn(10).minorUnits() == 60_000 && h.balanceOn(29).minorUnits() == 60_000);
        check("sum of balances over the month", h.sumOfBalances(0, 29).minorUnits() == 10 * 100_000L + 20 * 60_000L);
        check("average daily balance rounds half-even", h.averageDailyBalance(0, 29).minorUnits() == 73_333);   // 2,200,000 / 30 = 73333.33
        h.post(5, -7_500);
        check("backdated posting shifts 25 of 30 days", h.sumOfBalances(0, 29).minorUnits() == 2_200_000L - 25 * 7_500L);
        check("Money toString", h.balanceOn(29).toString().equals("525.00 USD"));
        check("divideHalfEven tie to even", BalanceHistory.divideHalfEven(25, 10) == 2 && BalanceHistory.divideHalfEven(35, 10) == 4);
        check("divideHalfEven negative", BalanceHistory.divideHalfEven(-25, 10) == -2 && BalanceHistory.divideHalfEven(-26, 10) == -3);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }

    static FenwickTree incremental(long[] values) {
        FenwickTree f = new FenwickTree(values.length);
        for (int i = 0; i < values.length; i++) f.add(i, values[i]);
        return f;
    }
}
