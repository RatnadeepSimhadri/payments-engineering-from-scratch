// Tests for phases/02-algorithms/02-binary-search-and-its-variants. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;
import java.util.Random;

public final class MainTest {
    private static int passed = 0;
    private static final List<String> failures = new ArrayList<>();

    static void check(String name, boolean condition) {
        if (condition) passed++; else failures.add(name);
    }

    static int linearLowerBound(long[] a, long t) { int i = 0; while (i < a.length && a[i] < t) i++; return i; }
    static int linearUpperBound(long[] a, long t) { int i = 0; while (i < a.length && a[i] <= t) i++; return i; }

    public static void main(String[] args) {
        long[] fees = {30, 30, 30, 50, 75, 75, 100, 250, 250, 250, 250, 500};
        long[] empty = {};
        long[] one = {42};

        // classic search
        check("indexOf finds a present key", fees[BinarySearch.indexOf(fees, 75)] == 75);
        check("indexOf unique key", BinarySearch.indexOf(fees, 100) == 6);
        check("indexOf absent key encodes insertion point", BinarySearch.indexOf(fees, 60) == -(4 + 1));
        check("indexOf below all", BinarySearch.indexOf(fees, 1) == -1);
        check("indexOf above all", BinarySearch.indexOf(fees, 9999) == -(fees.length + 1));
        check("indexOf empty", BinarySearch.indexOf(empty, 5) == -1);
        check("indexOf singleton hit", BinarySearch.indexOf(one, 42) == 0);
        check("indexOf singleton miss", BinarySearch.indexOf(one, 43) == -2);
        check("indexOf agrees with Arrays.binarySearch on absent keys", BinarySearch.indexOf(fees, 60) == Arrays.binarySearch(fees, 60));

        // lower / upper bound against a linear oracle on random sorted arrays
        Random rng = new Random(7);
        boolean allAgree = true;
        for (int trial = 0; trial < 200; trial++) {
            int n = rng.nextInt(40);
            long[] a = new long[n];
            for (int i = 0; i < n; i++) a[i] = rng.nextInt(20);
            Arrays.sort(a);
            for (long t = -1; t <= 20; t++) {
                if (BinarySearch.lowerBound(a, t) != linearLowerBound(a, t)) allAgree = false;
                if (BinarySearch.upperBound(a, t) != linearUpperBound(a, t)) allAgree = false;
            }
        }
        check("lowerBound/upperBound agree with linear scan on 200 random arrays", allAgree);
        check("lowerBound first of run", BinarySearch.lowerBound(fees, 250) == 7);
        check("upperBound one past run", BinarySearch.upperBound(fees, 250) == 11);
        check("lowerBound absent equals upperBound", BinarySearch.lowerBound(fees, 60) == 4 && BinarySearch.upperBound(fees, 60) == 4);
        check("lowerBound above all is length", BinarySearch.lowerBound(fees, 10_000) == fees.length);
        check("upperBound below all is 0", BinarySearch.upperBound(fees, 0) == 0);
        check("count of a run", BinarySearch.count(fees, 250) == 4);
        check("count of absent", BinarySearch.count(fees, 60) == 0);
        check("countInRange inclusive", BinarySearch.countInRange(fees, 30, 100) == 7);
        check("countInRange empty interval", BinarySearch.countInRange(fees, 51, 74) == 0);

        // firstTrue: monotone predicate primitive
        check("firstTrue finds boundary", BinarySearch.firstTrue(0, fees.length, i -> fees[i] > 200) == 7);
        check("firstTrue none true returns hi", BinarySearch.firstTrue(0, fees.length, i -> false) == fees.length);
        check("firstTrue all true returns lo", BinarySearch.firstTrue(0, fees.length, i -> true) == 0);
        check("firstTrue empty range returns lo", BinarySearch.firstTrue(5, 5, i -> true) == 5);
        check("firstTrueLong range", BinarySearch.firstTrueLong(0L, 10_000_000L, x -> x * x >= 1_000_000_000_000L) == 1_000_000L);

        // overflow-safe midpoint: a range whose lo + hi exceeds Integer.MAX_VALUE
        int[] calls = {0};
        int big = BinarySearch.firstTrue(1_500_000_000, 2_000_000_000, i -> { calls[0]++; return i >= 1_999_999_999; });
        check("firstTrue works on indices whose sum overflows int", big == 1_999_999_999);
        check("firstTrue takes about log2(range) steps", calls[0] <= 30);

        // rotated arrays
        long[] rotated = {40, 50, 60, 70, 10, 20, 30};
        check("rotationPoint", BinarySearch.rotationPoint(rotated) == 4);
        check("rotationPoint of unrotated array is 0", BinarySearch.rotationPoint(new long[] {1, 2, 3}) == 0);
        check("indexOfRotated hit in right part", BinarySearch.indexOfRotated(rotated, 20) == 5);
        check("indexOfRotated hit in left part", BinarySearch.indexOfRotated(rotated, 60) == 2);
        check("indexOfRotated miss", BinarySearch.indexOfRotated(rotated, 65) == -1);
        check("indexOfRotated empty", BinarySearch.indexOfRotated(empty, 1) == -1);
        boolean rotatedOk = true;
        for (int shift = 0; shift < 10; shift++) {
            long[] base = new long[10];
            for (int i = 0; i < 10; i++) base[i] = (i + shift) % 10 * 3;   // distinct, rotated by shift
            for (long t = 0; t < 30; t++) {
                int idx = BinarySearch.indexOfRotated(base, t);
                boolean present = t % 3 == 0;
                if (present && (idx < 0 || base[idx] != t)) rotatedOk = false;
                if (!present && idx != -1) rotatedOk = false;
            }
        }
        check("indexOfRotated correct for every rotation of 10 elements", rotatedOk);

        // search over the answer space
        check("integerSqrt exact square", BinarySearch.integerSqrt(1_000_000) == 1000);
        check("integerSqrt rounds down", BinarySearch.integerSqrt(999_999) == 999);
        check("integerSqrt 0 and 1", BinarySearch.integerSqrt(0) == 0 && BinarySearch.integerSqrt(1) == 1);
        check("integerSqrt Long.MAX_VALUE does not overflow", BinarySearch.integerSqrt(Long.MAX_VALUE) == 3_037_000_499L);
        boolean sqrtThrew = false;
        try { BinarySearch.integerSqrt(-1); } catch (ArithmeticException e) { sqrtThrew = true; }
        check("integerSqrt rejects negatives", sqrtThrew);

        // capped batch: the payments application
        Currency usd = Currency.USD;
        Money[] payouts = {Money.ofMinor(12_000, usd), Money.ofMinor(4_500, usd), Money.ofMinor(80_000, usd), Money.ofMinor(2_575, usd), Money.ofMinor(30_000, usd)};
        long cap = CappedBatch.largestCapWithin(payouts, Money.ofMinor(60_000, usd));
        check("cap keeps total within budget", CappedBatch.totalWithCap(payouts, cap).minorUnits() <= 60_000);
        check("cap + 1 exceeds budget (maximality)", CappedBatch.totalWithCap(payouts, cap + 1).minorUnits() > 60_000);
        check("cap value: 190.75 fixed + 2 capped items, (600.00 - 190.75) / 2 rounded down", cap == 20_462);
        check("budget larger than everything: cap is the largest item", CappedBatch.largestCapWithin(payouts, Money.ofMinor(1_000_000, usd)) == 80_000);
        check("zero budget: cap is 0", CappedBatch.largestCapWithin(payouts, Money.ofMinor(0, usd)) == 0);
        check("negative budget: -1 means infeasible", CappedBatch.largestCapWithin(payouts, Money.ofMinor(-1, usd)) == -1);
        boolean mismatch = false;
        try { CappedBatch.largestCapWithin(payouts, Money.ofMinor(1, new Currency("EUR", 2))); } catch (IllegalArgumentException e) { mismatch = true; }
        check("capped batch rejects currency mismatch", mismatch);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
