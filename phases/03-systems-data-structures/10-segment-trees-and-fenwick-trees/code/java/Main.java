// Lesson: phases/03-systems-data-structures/10-segment-trees-and-fenwick-trees/docs/en.md
// Segment Trees and Fenwick Trees
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: P. Fenwick, "A New Data Structure for Cumulative Frequency Tables" (Software: Practice and Experience, 1994);
// de Berg et al., "Computational Geometry", ch. 10 (segment trees). Java 21, standard library only.

import java.util.Arrays;
import java.util.Objects;
import java.util.Random;

/** ISO 4217 currency: code and minor-unit exponent. Copied from Phase 10, Lesson 01. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);

    Currency {
        if (code == null || code.length() != 3) throw new IllegalArgumentException("currency code must be three letters");
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4");
    }
}

/** Integer minor units plus a currency. The trees below hold raw minor units; Money is the display type. */
record Money(long minorUnits, Currency currency) {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    @Override public String toString() {
        long scale = 1;
        for (int i = 0; i < currency.exponent(); i++) scale *= 10;
        long abs = Math.abs(minorUnits);
        String whole = Long.toString(abs / scale);
        if (currency.exponent() == 0) return (minorUnits < 0 ? "-" : "") + whole + " " + currency.code();
        String frac = Long.toString(abs % scale);
        while (frac.length() < currency.exponent()) frac = "0" + frac;
        return (minorUnits < 0 ? "-" : "") + whole + "." + frac + " " + currency.code();
    }
}

/**
 * Fenwick tree (binary indexed tree) over n values: point add and prefix sum in O(log n), n+1 longs of storage.
 * Internally 1-indexed: tree[i] covers the (i & -i) values ending at position i. The public API is 0-indexed.
 */
final class FenwickTree {
    private final long[] tree;   // tree[0] unused
    private final int n;

    FenwickTree(int n) {
        if (n <= 0) throw new IllegalArgumentException("size must be positive: " + n);
        this.n = n;
        this.tree = new long[n + 1];
    }

    /** O(n) construction: each node pushes its total to its parent once. */
    static FenwickTree build(long[] values) {
        FenwickTree f = new FenwickTree(values.length);
        System.arraycopy(values, 0, f.tree, 1, values.length);
        for (int i = 1; i <= f.n; i++) {
            int parent = i + (i & -i);
            if (parent <= f.n) f.tree[parent] = Math.addExact(f.tree[parent], f.tree[i]);
        }
        return f;
    }

    int size() { return n; }

    /** values[index] += delta. Walks up: i += lowest set bit of i. */
    void add(int index, long delta) {
        Objects.checkIndex(index, n);
        for (int i = index + 1; i <= n; i += i & -i) tree[i] = Math.addExact(tree[i], delta);
    }

    /** Sum of values[0..index] inclusive. Walks down: i -= lowest set bit of i. Returns 0 for index -1. */
    long prefixSum(int index) {
        if (index < -1 || index >= n) throw new IndexOutOfBoundsException("index " + index + " for size " + n);
        long sum = 0;
        for (int i = index + 1; i > 0; i -= i & -i) sum = Math.addExact(sum, tree[i]);
        return sum;
    }

    /** Sum of values[lo..hi] inclusive, as the difference of two prefix sums. */
    long rangeSum(int lo, int hi) {
        if (lo > hi) throw new IllegalArgumentException("lo > hi: " + lo + " > " + hi);
        Objects.checkIndex(lo, n);
        Objects.checkIndex(hi, n);
        return Math.subtractExact(prefixSum(hi), prefixSum(lo - 1));
    }

    /** Current value at one position. */
    long get(int index) { return rangeSum(index, index); }

    /**
     * Smallest index whose prefix sum is >= target, or -1 if the total is smaller. Requires all values >= 0.
     * A binary descent over the implicit tree: O(log n) without calling prefixSum repeatedly.
     */
    int firstIndexWithPrefixAtLeast(long target) {
        if (target <= 0) return 0;
        int pos = 0;
        long remaining = target;
        int step = Integer.highestOneBit(n);
        for (; step > 0; step >>= 1) {
            int next = pos + step;
            if (next <= n && tree[next] < remaining) {
                pos = next;
                remaining -= tree[next];
            }
        }
        return pos < n ? pos : -1;     // pos is 1-indexed position of the last node that stayed below target
    }
}

/**
 * Segment tree over n values with point set and range sum, no laziness. Array-backed, 1-indexed heap layout:
 * node k covers a half-open range, children are 2k and 2k+1, leaves start at offset `size` (next power of two).
 */
final class SegmentTree {
    private final int n;
    private final int size;       // smallest power of two >= n
    private final long[] sum;     // 2 * size entries

    SegmentTree(long[] values) {
        if (values.length == 0) throw new IllegalArgumentException("size must be positive");
        this.n = values.length;
        int s = 1;
        while (s < n) s <<= 1;
        this.size = s;
        this.sum = new long[2 * s];
        System.arraycopy(values, 0, sum, s, n);
        for (int k = s - 1; k >= 1; k--) sum[k] = Math.addExact(sum[2 * k], sum[2 * k + 1]);
    }

    int size() { return n; }

    /** values[index] = value; recompute the log n ancestors. */
    void set(int index, long value) {
        Objects.checkIndex(index, n);
        int k = index + size;
        sum[k] = value;
        for (k >>= 1; k >= 1; k >>= 1) sum[k] = Math.addExact(sum[2 * k], sum[2 * k + 1]);
    }

    long get(int index) {
        Objects.checkIndex(index, n);
        return sum[index + size];
    }

    /** Sum of values[lo..hi] inclusive. Bottom-up: shrink both ends toward each other, adding whole nodes. */
    long rangeSum(int lo, int hi) {
        if (lo > hi) throw new IllegalArgumentException("lo > hi: " + lo + " > " + hi);
        Objects.checkIndex(lo, n);
        Objects.checkIndex(hi, n);
        long left = 0, right = 0;
        int l = lo + size, r = hi + size + 1;              // half-open [l, r)
        while (l < r) {
            if ((l & 1) == 1) left = Math.addExact(left, sum[l++]);
            if ((r & 1) == 1) right = Math.addExact(sum[--r], right);
            l >>= 1;
            r >>= 1;
        }
        return Math.addExact(left, right);
    }
}

/**
 * Segment tree with lazy propagation: range add and range sum, both O(log n). Recursive, 1-indexed, node k
 * covers [nodeLo, nodeHi]. pending[k] is an addend that applies to every value under k but has not yet been
 * pushed to k's children.
 */
final class LazySegmentTree {
    private final int n;
    private final long[] sum;
    private final long[] pending;

    LazySegmentTree(long[] values) {
        if (values.length == 0) throw new IllegalArgumentException("size must be positive");
        this.n = values.length;
        this.sum = new long[4 * n];
        this.pending = new long[4 * n];
        build(1, 0, n - 1, values);
    }

    private void build(int k, int lo, int hi, long[] values) {
        if (lo == hi) { sum[k] = values[lo]; return; }
        int mid = (lo + hi) >>> 1;
        build(2 * k, lo, mid, values);
        build(2 * k + 1, mid + 1, hi, values);
        sum[k] = Math.addExact(sum[2 * k], sum[2 * k + 1]);
    }

    int size() { return n; }

    /** Apply a pending addend to node k covering `count` values: the sum grows by count * delta. */
    private void apply(int k, int count, long delta) {
        sum[k] = Math.addExact(sum[k], Math.multiplyExact(delta, count));
        pending[k] = Math.addExact(pending[k], delta);
    }

    /** Push k's pending addend one level down so that its children are exact before we look at them. */
    private void push(int k, int lo, int hi) {
        if (pending[k] == 0) return;
        int mid = (lo + hi) >>> 1;
        apply(2 * k, mid - lo + 1, pending[k]);
        apply(2 * k + 1, hi - mid, pending[k]);
        pending[k] = 0;
    }

    /** values[i] += delta for every i in [lo, hi]. */
    void rangeAdd(int lo, int hi, long delta) {
        if (lo > hi) throw new IllegalArgumentException("lo > hi: " + lo + " > " + hi);
        Objects.checkIndex(lo, n);
        Objects.checkIndex(hi, n);
        rangeAdd(1, 0, n - 1, lo, hi, delta);
    }

    private void rangeAdd(int k, int nodeLo, int nodeHi, int lo, int hi, long delta) {
        if (hi < nodeLo || nodeHi < lo) return;                       // disjoint
        if (lo <= nodeLo && nodeHi <= hi) { apply(k, nodeHi - nodeLo + 1, delta); return; }   // fully covered: stop here
        push(k, nodeLo, nodeHi);
        int mid = (nodeLo + nodeHi) >>> 1;
        rangeAdd(2 * k, nodeLo, mid, lo, hi, delta);
        rangeAdd(2 * k + 1, mid + 1, nodeHi, lo, hi, delta);
        sum[k] = Math.addExact(sum[2 * k], sum[2 * k + 1]);
    }

    /** Sum of values[lo..hi] inclusive. */
    long rangeSum(int lo, int hi) {
        if (lo > hi) throw new IllegalArgumentException("lo > hi: " + lo + " > " + hi);
        Objects.checkIndex(lo, n);
        Objects.checkIndex(hi, n);
        return rangeSum(1, 0, n - 1, lo, hi);
    }

    private long rangeSum(int k, int nodeLo, int nodeHi, int lo, int hi) {
        if (hi < nodeLo || nodeHi < lo) return 0;
        if (lo <= nodeLo && nodeHi <= hi) return sum[k];
        push(k, nodeLo, nodeHi);
        int mid = (nodeLo + nodeHi) >>> 1;
        return Math.addExact(rangeSum(2 * k, nodeLo, mid, lo, hi), rangeSum(2 * k + 1, mid + 1, nodeHi, lo, hi));
    }

    long get(int index) { return rangeSum(index, index); }
}

/**
 * The payments framing: one account's closing balance for each day of a period, with backdated postings
 * and average-daily-balance queries. Backed by a LazySegmentTree over balances: a posting on day d shifts
 * every balance from d to the end of the period, and a statement needs the sum of balances over a month.
 */
final class BalanceHistory {
    private final LazySegmentTree balances;
    private final Currency currency;

    BalanceHistory(long openingBalanceMinor, int days, Currency currency) {
        long[] initial = new long[days];
        Arrays.fill(initial, openingBalanceMinor);
        this.balances = new LazySegmentTree(initial);
        this.currency = currency;
    }

    int days() { return balances.size(); }

    /** Post an amount effective on `day`: every closing balance from that day onward moves by the amount. */
    void post(int day, long deltaMinor) { balances.rangeAdd(day, balances.size() - 1, deltaMinor); }

    Money balanceOn(int day) { return Money.ofMinor(balances.get(day), currency); }

    /** Sum of closing balances over [fromDay, toDay], the numerator of average daily balance. */
    Money sumOfBalances(int fromDay, int toDay) { return Money.ofMinor(balances.rangeSum(fromDay, toDay), currency); }

    /** Average daily balance over [fromDay, toDay], rounded half-even to a minor unit. */
    Money averageDailyBalance(int fromDay, int toDay) {
        long total = balances.rangeSum(fromDay, toDay);
        long days = toDay - fromDay + 1;
        return Money.ofMinor(divideHalfEven(total, days), currency);
    }

    static long divideHalfEven(long dividend, long divisor) {
        long q = dividend / divisor, r = dividend % divisor;
        if (r == 0) return q;
        int sign = (dividend < 0) ^ (divisor < 0) ? -1 : 1;
        long twice = Math.abs(r) * 2, d = Math.abs(divisor);
        if (twice > d) return q + sign;
        if (twice < d) return q;
        return (q % 2 == 0) ? q : q + sign;
    }
}

public final class Main {
    /** Reference implementation: the thing the trees must agree with on every query. */
    static long naiveRangeSum(long[] values, int lo, int hi) {
        long s = 0;
        for (int i = lo; i <= hi; i++) s += values[i];
        return s;
    }

    public static void main(String[] args) {
        System.out.println("== 1. Fenwick tree: daily net postings, balance by prefix sum ==");
        long[] dailyNet = {12_500, -3_200, 0, 4_150, -900, 7_000, -2_250, 1_000};   // minor units, 8 days
        FenwickTree fw = FenwickTree.build(dailyNet);
        System.out.println("  daily net   = " + Arrays.toString(dailyNet));
        StringBuilder prefixes = new StringBuilder();
        for (int d = 0; d < dailyNet.length; d++) prefixes.append(d == 0 ? "" : ", ").append(fw.prefixSum(d));
        System.out.println("  balances    = [" + prefixes + "]");
        System.out.println("  net days 2..5 = " + Money.ofMinor(fw.rangeSum(2, 5), Currency.USD));
        System.out.println("  backdated correction: day 1 was one dollar more negative than recorded, add(1, -100)");
        fw.add(1, -100);
        System.out.println("  balance day 1 = " + Money.ofMinor(fw.prefixSum(1), Currency.USD)
                + ", day 7 = " + Money.ofMinor(fw.prefixSum(7), Currency.USD) + " (every later balance moved by one dollar)");
        System.out.println("  tree[] internals (1-indexed, node i covers i & -i values): " + fenwickInternals(fw, dailyNet.length));

        System.out.println();
        System.out.println("== 2. Segment tree: same queries, point set instead of point add ==");
        SegmentTree st = new SegmentTree(dailyNet);
        System.out.println("  sum days 2..5 before = " + st.rangeSum(2, 5));
        st.set(3, 10_000);
        System.out.println("  set day 3 to 10000: sum days 2..5 after = " + st.rangeSum(2, 5) + ", naive = " + naiveRangeSum(new long[]{12_500, -3_200, 0, 10_000, -900, 7_000, -2_250, 1_000}, 2, 5));

        System.out.println();
        System.out.println("== 3. Lazy segment tree: backdated posting = range add over closing balances ==");
        BalanceHistory history = new BalanceHistory(100_000, 30, Currency.USD);   // 1000.00 USD opening, 30 days
        history.post(0, 12_500);      // +125.00 on day 0
        history.post(10, -40_000);    // -400.00 on day 10
        history.post(20, 5_000);      // +50.00 on day 20
        System.out.println("  balance day 0 = " + history.balanceOn(0) + ", day 10 = " + history.balanceOn(10)
                + ", day 29 = " + history.balanceOn(29));
        System.out.println("  average daily balance days 0..29 = " + history.averageDailyBalance(0, 29));
        System.out.println("  a backdated posting arrives: -75.00 effective day 5");
        history.post(5, -7_500);
        System.out.println("  balance day 4 = " + history.balanceOn(4) + ", day 5 = " + history.balanceOn(5)
                + ", day 29 = " + history.balanceOn(29));
        System.out.println("  average daily balance days 0..29 = " + history.averageDailyBalance(0, 29)
                + " (25 of 30 days moved by -75.00: 25 * 75 / 30 = 62.50 lower)");
        System.out.println("  one rangeAdd touched O(log 30) nodes, not 25 days");

        System.out.println();
        System.out.println("== 4. Fenwick descent: which day does cumulative volume cross 20000 minor units? ==");
        long[] volume = {5_000, 3_000, 8_000, 2_000, 6_000, 1_000};
        FenwickTree vol = FenwickTree.build(volume);
        System.out.println("  volume      = " + Arrays.toString(volume));
        System.out.println("  first index with prefix >= 20000 = " + vol.firstIndexWithPrefixAtLeast(20_000) + " (prefix sums 5000, 8000, 16000, 18000, 24000)");
        System.out.println("  first index with prefix >= 25001 = " + vol.firstIndexWithPrefixAtLeast(25_001) + " (total is 25000)");

        System.out.println();
        System.out.println("== 5. Randomized agreement: 20000 mixed operations against the naive array ==");
        Random rng = new Random(42);
        int n = 257;                                 // deliberately not a power of two
        long[] model = new long[n];
        LazySegmentTree lazy = new LazySegmentTree(model);
        FenwickTree fenwick = new FenwickTree(n);
        int mismatches = 0;
        for (int op = 0; op < 20_000; op++) {
            int a = rng.nextInt(n), b = rng.nextInt(n);
            int lo = Math.min(a, b), hi = Math.max(a, b);
            if (rng.nextBoolean()) {
                long delta = rng.nextInt(2001) - 1000;
                for (int i = lo; i <= hi; i++) { model[i] += delta; fenwick.add(i, delta); }
                lazy.rangeAdd(lo, hi, delta);
            } else {
                long expect = naiveRangeSum(model, lo, hi);
                if (lazy.rangeSum(lo, hi) != expect || fenwick.rangeSum(lo, hi) != expect) mismatches++;
            }
        }
        System.out.println("  mismatches = " + mismatches);

        System.out.println();
        System.out.println("== 6. Cost: 10 million days, one query ==");
        int big = 10_000_000;
        System.out.println("  naive range sum: up to " + big + " additions; Fenwick: 2 * " + (32 - Integer.numberOfLeadingZeros(big)) + " node reads; segment tree: about 2 * " + (32 - Integer.numberOfLeadingZeros(big)) + " node reads");
        System.out.println("  memory: Fenwick " + (big + 1) + " longs; segment tree " + (2 * Integer.highestOneBit(big) * 2) + " longs; lazy tree 2 x " + (4 * big) + " longs");
    }

    static String fenwickInternals(FenwickTree fw, int n) {
        StringBuilder sb = new StringBuilder("[");
        for (int i = 1; i <= n; i++) {
            int span = i & -i;
            sb.append(i > 1 ? ", " : "").append("t").append(i).append("=sum(").append(i - span).append("..").append(i - 1).append(")");
        }
        return sb.append("]").toString();
    }
}
