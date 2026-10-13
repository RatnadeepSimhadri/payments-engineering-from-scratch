// Lesson: phases/02-algorithms/03-two-pointers-and-sliding-windows/docs/en.md
// Two Pointers and Sliding Windows
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Cormen et al., Introduction to Algorithms (amortized analysis of two-index scans); the monotonic deque
// for sliding-window maximum follows the standard formulation (e.g. Tarjan, "Amortized Computational Complexity").
// Java 21, standard library only.

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;
import java.util.Objects;

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
    Money minus(Money o) { requireSame(o); return new Money(Math.subtractExact(minorUnits, o.minorUnits), currency); }

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

/** One card authorization attempt: when it happened (ms since epoch) and for how much. Events arrive in time order. */
record Event(long timestampMs, Money amount) {}

/** Two indices moving over one sorted array. */
final class TwoPointers {
    private TwoPointers() {}

    /** Indices (i, j), i < j, with a[i] + a[j] == target in a sorted array, or null. O(n). */
    static int[] pairWithSum(long[] sorted, long target) {
        int i = 0, j = sorted.length - 1;
        while (i < j) {
            long sum = sorted[i] + sorted[j];
            if (sum == target) return new int[] {i, j};
            if (sum < target) i++;      // the smallest element can never pair with anything: drop it
            else j--;                   // the largest element can never pair with anything: drop it
        }
        return null;
    }

    /** Remove duplicates from a sorted array in place; returns the new length. Reader and writer pointers. */
    static int dedupeSorted(long[] sorted) {
        if (sorted.length == 0) return 0;
        int write = 1;
        for (int read = 1; read < sorted.length; read++) {
            if (sorted[read] != sorted[write - 1]) sorted[write++] = sorted[read];
        }
        return write;
    }

    /** Merge two sorted arrays into one sorted array. The merge step from Lesson 01, standing alone. */
    static long[] mergeSorted(long[] a, long[] b) {
        long[] out = new long[a.length + b.length];
        int i = 0, j = 0, k = 0;
        while (i < a.length && j < b.length) out[k++] = a[i] <= b[j] ? a[i++] : b[j++];
        while (i < a.length) out[k++] = a[i++];
        while (j < b.length) out[k++] = b[j++];
        return out;
    }
}

/** Windows over an array or an event stream. Each method is O(n) total because both ends only move forward. */
final class SlidingWindow {
    private SlidingWindow() {}

    /** Fixed width k: the maximum sum of any k consecutive elements. Throws if k is out of range. */
    static long maxSumFixed(long[] a, int k) {
        if (k <= 0 || k > a.length) throw new IllegalArgumentException("k must be in 1.." + a.length + ": " + k);
        long sum = 0;
        for (int i = 0; i < k; i++) sum += a[i];
        long best = sum;
        for (int right = k; right < a.length; right++) {
            sum += a[right] - a[right - k];     // slide: add the new element, drop the one that fell off the left
            best = Math.max(best, sum);
        }
        return best;
    }

    /** Variable width: the length of the shortest contiguous run with sum >= target; 0 if none. Non-negative values only. */
    static int shortestRunWithSumAtLeast(long[] a, long target) {
        for (long v : a) if (v < 0) throw new IllegalArgumentException("values must be non-negative for the window to be monotone");
        int best = 0, left = 0;
        long sum = 0;
        for (int right = 0; right < a.length; right++) {
            sum += a[right];
            while (sum >= target && left <= right) {           // shrink from the left while the window still qualifies
                int len = right - left + 1;
                if (best == 0 || len < best) best = len;
                sum -= a[left++];
            }
        }
        return best;
    }

    /** Longest substring with no repeated character; the "at most one of each" window. */
    static int longestWithoutRepeat(String s) {
        int[] lastSeen = new int[Character.MAX_VALUE + 1];
        Arrays.fill(lastSeen, -1);
        int best = 0, left = 0;
        for (int right = 0; right < s.length(); right++) {
            char c = s.charAt(right);
            if (lastSeen[c] >= left) left = lastSeen[c] + 1;    // jump left past the previous copy of c
            lastSeen[c] = right;
            best = Math.max(best, right - left + 1);
        }
        return best;
    }

    /**
     * Sliding-window maximum with a monotonic deque: for every window of width k, the largest element.
     * The deque holds indices whose values are strictly decreasing front to back; the front is always the max.
     */
    static long[] maxOfEachWindow(long[] a, int k) {
        if (k <= 0 || k > a.length) throw new IllegalArgumentException("k must be in 1.." + a.length + ": " + k);
        long[] out = new long[a.length - k + 1];
        ArrayDeque<Integer> dq = new ArrayDeque<>();
        for (int i = 0; i < a.length; i++) {
            while (!dq.isEmpty() && a[dq.peekLast()] <= a[i]) dq.pollLast();   // a[i] dominates everything smaller before it
            dq.addLast(i);
            if (dq.peekFirst() <= i - k) dq.pollFirst();                        // the front fell out of the window
            if (i >= k - 1) out[i - k + 1] = a[dq.peekFirst()];
        }
        return out;
    }
}

/**
 * A velocity check: "no more than maxCount authorizations and no more than maxTotal in any trailing window of
 * windowMs". Events are fed in timestamp order; the window is the last windowMs of events, inclusive of the newest.
 * This is the shape of every card-testing and account-takeover rule in a fraud engine.
 */
final class VelocityChecker {
    private final long windowMs;
    private final int maxCount;
    private final Money maxTotal;
    private final ArrayDeque<Event> window = new ArrayDeque<>();
    private Money total;
    private long lastTimestamp = Long.MIN_VALUE;

    VelocityChecker(long windowMs, int maxCount, Money maxTotal) {
        if (windowMs <= 0 || maxCount <= 0) throw new IllegalArgumentException("window and count must be positive");
        this.windowMs = windowMs;
        this.maxCount = maxCount;
        this.maxTotal = maxTotal;
        this.total = Money.ofMinor(0, maxTotal.currency());
    }

    enum Decision { ALLOW, DENY_COUNT, DENY_AMOUNT }

    /** Record the event and decide. Denied events still count: an attacker's failed attempts are the signal. */
    Decision record(Event e) {
        if (e.timestampMs() < lastTimestamp) throw new IllegalArgumentException("events must arrive in time order");
        lastTimestamp = e.timestampMs();
        evictOlderThan(e.timestampMs() - windowMs);
        window.addLast(e);
        total = total.plus(e.amount());
        if (window.size() > maxCount) return Decision.DENY_COUNT;
        if (total.compareTo(maxTotal) > 0) return Decision.DENY_AMOUNT;
        return Decision.ALLOW;
    }

    private void evictOlderThan(long cutoffExclusive) {
        while (!window.isEmpty() && window.peekFirst().timestampMs() <= cutoffExclusive) {
            total = total.minus(window.pollFirst().amount());
        }
    }

    int countInWindow() { return window.size(); }

    Money totalInWindow() { return total; }
}

public final class Main {
    public static void main(String[] args) {
        Currency usd = Currency.USD;

        System.out.println("== Two pointers on a sorted array ==");
        long[] amounts = {75, 500, 500, 1999, 1999, 2575, 4500, 12000};
        int[] pair = TwoPointers.pairWithSum(amounts, 4574);
        System.out.println("pairWithSum(4574) -> indices " + Arrays.toString(pair) + " = " + amounts[pair[0]] + " + " + amounts[pair[1]]);
        System.out.println("pairWithSum(1)    -> " + Arrays.toString(TwoPointers.pairWithSum(amounts, 1)));
        long[] copy = amounts.clone();
        int n = TwoPointers.dedupeSorted(copy);
        System.out.println("dedupeSorted      -> " + Arrays.toString(Arrays.copyOf(copy, n)));
        System.out.println("mergeSorted       -> " + Arrays.toString(TwoPointers.mergeSorted(new long[] {1, 4, 7}, new long[] {2, 3, 9})));

        System.out.println();
        System.out.println("== Fixed window: best 3-day settlement total (cents per day) ==");
        long[] daily = {12000, 4500, 80000, 2575, 30000, 1999, 500, 75};
        System.out.println("days: " + Arrays.toString(daily));
        System.out.println("maxSumFixed(k=3) = " + SlidingWindow.maxSumFixed(daily, 3) + " (days 2..4: 80000 + 2575 + 30000)");

        System.out.println();
        System.out.println("== Variable window: shortest run of days totalling at least 100,000 cents ==");
        System.out.println("shortestRunWithSumAtLeast(100000) = " + SlidingWindow.shortestRunWithSumAtLeast(daily, 100_000) + " days");
        System.out.println("shortestRunWithSumAtLeast(1000000) = " + SlidingWindow.shortestRunWithSumAtLeast(daily, 1_000_000) + " (impossible -> 0)");
        System.out.println("longestWithoutRepeat(\"abcabcbb\") = " + SlidingWindow.longestWithoutRepeat("abcabcbb"));

        System.out.println();
        System.out.println("== Sliding-window maximum with a monotonic deque, k = 3 ==");
        long[] prices = {1, 3, -1, -3, 5, 3, 6, 7};
        System.out.println("input  " + Arrays.toString(prices));
        System.out.println("maxima " + Arrays.toString(SlidingWindow.maxOfEachWindow(prices, 3)));

        System.out.println();
        System.out.println("== Velocity check: max 4 authorizations and 500.00 USD per trailing 60 s ==");
        VelocityChecker vc = new VelocityChecker(60_000, 4, Money.ofMinor(50_000, usd));
        long t0 = 1_700_000_000_000L;
        Object[][] script = {
            {t0 + 0, 12_000L}, {t0 + 10_000, 4_500L}, {t0 + 20_000, 25_000L}, {t0 + 25_000, 1_999L},
            {t0 + 30_000, 100L}, {t0 + 70_000, 30_000L}, {t0 + 125_000, 75L},
        };
        for (Object[] row : script) {
            long ts = (Long) row[0];
            Event e = new Event(ts, Money.ofMinor((Long) row[1], usd));
            VelocityChecker.Decision d = vc.record(e);
            System.out.printf("  t=+%3ds  %-10s -> %-12s window: %d events, %s%n",
                    (ts - t0) / 1000, e.amount(), d, vc.countInWindow(), vc.totalInWindow());
        }
    }
}
