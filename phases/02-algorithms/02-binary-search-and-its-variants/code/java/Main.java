// Lesson: phases/02-algorithms/02-binary-search-and-its-variants/docs/en.md
// Binary Search and Its Variants
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Bentley, "Programming Pearls" ch. 4 (binary search correctness); Bloch, "Extra, Extra - Read All
// About It: Nearly All Binary Searches and Mergesorts are Broken" (2006, overflow-safe midpoint). Java 21, stdlib only.

import java.util.Objects;
import java.util.function.IntPredicate;
import java.util.function.LongPredicate;

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

/**
 * Every search in this class is the same loop: maintain an invariant over a half-open range [lo, hi),
 * shrink it by half per step, stop when it is empty. The variants differ only in the predicate.
 */
final class BinarySearch {
    private BinarySearch() {}

    /** Classic: index of `target` in a sorted array, or -(insertionPoint + 1) if absent (the JDK convention). */
    static int indexOf(long[] a, long target) {
        int lo = 0, hi = a.length - 1;          // closed range [lo, hi]
        while (lo <= hi) {
            int mid = lo + ((hi - lo) >>> 1);   // never overflows; (lo + hi) / 2 does at 2^30 elements
            if (a[mid] < target) lo = mid + 1;
            else if (a[mid] > target) hi = mid - 1;
            else return mid;
        }
        return -(lo + 1);
    }

    /**
     * The one primitive everything else is built from: the smallest index i in [lo, hi) such that
     * predicate(i) is true, or hi if none. Requires the predicate to be monotone: false...false true...true.
     */
    static int firstTrue(int lo, int hi, IntPredicate predicate) {
        while (lo < hi) {
            int mid = lo + ((hi - lo) >>> 1);
            if (predicate.test(mid)) hi = mid;   // mid is a candidate: keep it in the range
            else lo = mid + 1;                   // mid is false: the answer is strictly to the right
        }
        return lo;                               // invariant: everything < lo is false, everything >= hi is true
    }

    /** Same primitive over a range of long values rather than indices (for search over an answer space). */
    static long firstTrueLong(long lo, long hi, LongPredicate predicate) {
        while (lo < hi) {
            long mid = lo + ((hi - lo) >>> 1);
            if (predicate.test(mid)) hi = mid;
            else lo = mid + 1;
        }
        return lo;
    }

    /** First index whose value is >= target: the insertion point before any existing equal keys. */
    static int lowerBound(long[] a, long target) {
        return firstTrue(0, a.length, i -> a[i] >= target);
    }

    /** First index whose value is > target: one past the last equal key. */
    static int upperBound(long[] a, long target) {
        return firstTrue(0, a.length, i -> a[i] > target);
    }

    /** Number of elements equal to target, in O(log n) via the two bounds. */
    static int count(long[] a, long target) {
        return upperBound(a, target) - lowerBound(a, target);
    }

    /** Number of elements in the closed value range [low, high]. */
    static int countInRange(long[] a, long low, long high) {
        return upperBound(a, high) - lowerBound(a, low);
    }

    /** Index of the minimum in an array that was sorted ascending, then rotated; distinct values required. */
    static int rotationPoint(long[] a) {
        if (a.length == 0) throw new IllegalArgumentException("empty array");
        long last = a[a.length - 1];
        // predicate "a[i] <= last" is false on the rotated prefix (all larger than last) and true from the minimum on
        return firstTrue(0, a.length, i -> a[i] <= last);
    }

    /** Search a rotated sorted array without un-rotating it: locate the pivot, then search the correct half. */
    static int indexOfRotated(long[] a, long target) {
        if (a.length == 0) return -1;
        int pivot = rotationPoint(a);
        int n = a.length;
        // the array is sorted when read as a[(pivot + k) % n] for k in [0, n)
        int k = firstTrue(0, n, i -> a[(pivot + i) % n] >= target);
        if (k == n || a[(pivot + k) % n] != target) return -1;
        return (pivot + k) % n;
    }

    /** Largest x >= 0 with x * x <= n: search over the answer space, not over an array. */
    static long integerSqrt(long n) {
        if (n < 0) throw new ArithmeticException("negative");
        // first x such that x*x > n, minus one; the guard x > n / x avoids overflow of x*x
        long first = firstTrueLong(0, 3_037_000_500L, x -> x > 0 && x > n / x);
        return first - 1;
    }
}

/**
 * A payments application of search over the answer space: the smallest per-item cap such that
 * capping every line item at it leaves the batch total within a budget. Monotone: raising the cap never lowers the total.
 */
final class CappedBatch {
    private CappedBatch() {}

    static Money totalWithCap(Money[] items, long capMinor) {
        long total = 0;
        for (Money m : items) total = Math.addExact(total, Math.min(m.minorUnits(), capMinor));
        return Money.ofMinor(total, items[0].currency());
    }

    /** Largest cap (in minor units) such that totalWithCap <= budget; -1 if even a cap of 0 exceeds the budget. */
    static long largestCapWithin(Money[] items, Money budget) {
        if (items.length == 0) throw new IllegalArgumentException("no items");
        for (Money m : items) if (!m.currency().equals(budget.currency())) throw new IllegalArgumentException("currency mismatch");
        long maxItem = 0;
        for (Money m : items) maxItem = Math.max(maxItem, m.minorUnits());
        // predicate: "a cap of c already exceeds the budget"; false for small c, true for large c
        long firstOver = BinarySearch.firstTrueLong(0, maxItem + 1, c -> totalWithCap(items, c).compareTo(budget) > 0);
        return firstOver - 1;
    }
}

public final class Main {
    static String fmt(long[] a) {
        StringBuilder sb = new StringBuilder("[");
        for (int i = 0; i < a.length; i++) sb.append(i == 0 ? "" : ", ").append(a[i]);
        return sb.append("]").toString();
    }

    public static void main(String[] args) {
        long[] fees = {30, 30, 30, 50, 75, 75, 100, 250, 250, 250, 250, 500};
        System.out.println("== Sorted fee schedule (cents): " + fmt(fees) + " ==");
        System.out.println("indexOf(75)       = " + BinarySearch.indexOf(fees, 75) + "   (some index of a 75; which one is unspecified)");
        System.out.println("indexOf(60)       = " + BinarySearch.indexOf(fees, 60) + "   (negative: insertion point is " + (-BinarySearch.indexOf(fees, 60) - 1) + ")");
        System.out.println("lowerBound(250)   = " + BinarySearch.lowerBound(fees, 250) + "   (first 250)");
        System.out.println("upperBound(250)   = " + BinarySearch.upperBound(fees, 250) + "  (one past the last 250)");
        System.out.println("count(250)        = " + BinarySearch.count(fees, 250));
        System.out.println("count(60)         = " + BinarySearch.count(fees, 60) + "   (absent: lowerBound == upperBound)");
        System.out.println("countInRange(30, 100) = " + BinarySearch.countInRange(fees, 30, 100));

        System.out.println();
        System.out.println("== Overflow-safe midpoint ==");
        int lo = 1_500_000_000, hi = 2_000_000_000;
        System.out.println("(lo + hi) / 2            = " + ((lo + hi) / 2) + "   (wrapped negative)");
        System.out.println("lo + ((hi - lo) >>> 1)   = " + (lo + ((hi - lo) >>> 1)));

        System.out.println();
        System.out.println("== firstTrue over a monotone predicate ==");
        int firstOver200 = BinarySearch.firstTrue(0, fees.length, i -> fees[i] > 200);
        System.out.println("first index with fee > 200: " + firstOver200 + " (value " + fees[firstOver200] + ")");
        System.out.println("none true -> returns hi:    " + BinarySearch.firstTrue(0, fees.length, i -> fees[i] > 10_000));

        System.out.println();
        System.out.println("== Rotated sorted array ==");
        long[] rotated = {40, 50, 60, 70, 10, 20, 30};
        System.out.println("array           = " + fmt(rotated));
        System.out.println("rotationPoint   = " + BinarySearch.rotationPoint(rotated) + " (index of the minimum)");
        System.out.println("indexOfRotated(20) = " + BinarySearch.indexOfRotated(rotated, 20));
        System.out.println("indexOfRotated(65) = " + BinarySearch.indexOfRotated(rotated, 65));

        System.out.println();
        System.out.println("== Search over the answer space ==");
        System.out.println("integerSqrt(1_000_000_007) = " + BinarySearch.integerSqrt(1_000_000_007L));
        System.out.println("integerSqrt(Long.MAX_VALUE) = " + BinarySearch.integerSqrt(Long.MAX_VALUE));
        Currency usd = Currency.USD;
        Money[] payouts = {Money.ofMinor(12_000, usd), Money.ofMinor(4_500, usd), Money.ofMinor(80_000, usd), Money.ofMinor(2_575, usd), Money.ofMinor(30_000, usd)};
        Money budget = Money.ofMinor(60_000, usd);
        long cap = CappedBatch.largestCapWithin(payouts, budget);
        System.out.println("payouts: 120.00, 45.00, 800.00, 25.75, 300.00; budget 600.00 USD");
        System.out.println("largest per-payout cap within budget = " + Money.ofMinor(cap, usd)
                + " -> total " + CappedBatch.totalWithCap(payouts, cap)
                + "; one cent more -> " + CappedBatch.totalWithCap(payouts, cap + 1));
    }
}
