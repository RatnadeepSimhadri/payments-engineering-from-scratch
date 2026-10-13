// Lesson: phases/02-algorithms/13-interval-problems/docs/en.md
// Interval Problems: Merging, Scheduling, and Sweep Lines
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Kleinberg & Tardos, Algorithm Design (2005), ch. 4 (interval scheduling exchange argument);
// Shamos & Hoey, "Geometric intersection problems" (FOCS 1976) for the sweep line; ISO 4217 for Money.
// Java 21, standard library only.

import java.util.ArrayList;
import java.util.Collections;
import java.util.Comparator;
import java.util.List;
import java.util.Objects;

/** ISO 4217 currency: code and minor-unit exponent. Copied from Phase 10, Lesson 01. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency EUR = new Currency("EUR", 2);

    Currency {
        if (code == null || code.length() != 3 || !code.chars().allMatch(Character::isUpperCase)) {
            throw new IllegalArgumentException("currency code must be three uppercase letters: " + code);
        }
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4: " + exponent);
    }
}

/** Exact amount in minor units of one currency. Subset of Phase 10, Lesson 01: what this lesson needs. */
record Money(long minorUnits, Currency currency) implements Comparable<Money> {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }
    static Money zero(Currency currency) { return new Money(0, currency); }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) {
            throw new IllegalArgumentException("cannot combine " + currency.code() + " with " + other.currency.code());
        }
    }

    Money plus(Money other) { requireSameCurrency(other); return new Money(Math.addExact(minorUnits, other.minorUnits), currency); }
    Money minus(Money other) { requireSameCurrency(other); return new Money(Math.subtractExact(minorUnits, other.minorUnits), currency); }

    @Override public int compareTo(Money other) { requireSameCurrency(other); return Long.compare(minorUnits, other.minorUnits); }

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
 * A half-open interval [start, end) on an integer timeline (seconds, minutes, epoch millis: the unit is the
 * caller's). Half-open means [0, 10) and [10, 20) touch but do not overlap, which is what a timeline needs:
 * every instant belongs to exactly one of two adjacent intervals.
 */
record Interval(long start, long end) implements Comparable<Interval> {
    Interval {
        if (end < start) throw new IllegalArgumentException("end " + end + " is before start " + start);
    }

    long length() { return end - start; }
    boolean isEmpty() { return end == start; }

    /** True if the two intervals share at least one instant. Touching intervals ([0,10) and [10,20)) do not. */
    boolean overlaps(Interval o) { return start < o.end && o.start < end; }

    /** True if they overlap or touch, i.e. their union is a single interval. */
    boolean overlapsOrTouches(Interval o) { return start <= o.end && o.start <= end; }

    boolean contains(long t) { return start <= t && t < end; }

    /** The overlapping part, or an empty interval at the later start if there is none. */
    Interval intersect(Interval o) {
        long s = Math.max(start, o.start), e = Math.min(end, o.end);
        return e < s ? new Interval(s, s) : new Interval(s, e);
    }

    /** Sort by start, then by end. The order every algorithm below relies on. */
    @Override public int compareTo(Interval o) {
        int c = Long.compare(start, o.start);
        return c != 0 ? c : Long.compare(end, o.end);
    }

    @Override public String toString() { return "[" + start + ", " + end + ")"; }
}

/** The result of a maximum-overlap sweep: how many intervals were open at once, and the first instant it happened. */
record Peak(int count, long at) {}

/** Interval algorithms on half-open integer intervals. Every method is pure and leaves its input untouched. */
final class Intervals {
    private Intervals() {}

    /**
     * Merge overlapping or touching intervals into a disjoint, sorted list. Sort by start; then each interval
     * either extends the last merged one or starts a new one. O(n log n) for the sort, O(n) after.
     * Invariant of the output: strictly increasing starts, and merged[i].end < merged[i+1].start.
     */
    static List<Interval> merge(List<Interval> input) {
        List<Interval> sorted = new ArrayList<>(input);
        Collections.sort(sorted);
        List<Interval> out = new ArrayList<>();
        for (Interval iv : sorted) {
            if (iv.isEmpty()) continue;
            if (out.isEmpty() || !out.get(out.size() - 1).overlapsOrTouches(iv)) {
                out.add(iv);
            } else {
                Interval last = out.remove(out.size() - 1);
                out.add(new Interval(last.start(), Math.max(last.end(), iv.end())));
            }
        }
        return out;
    }

    /** Total length covered by the union of the intervals: the sum of the merged lengths. */
    static long coverage(List<Interval> input) {
        long total = 0;
        for (Interval iv : merge(input)) total = Math.addExact(total, iv.length());
        return total;
    }

    /** The parts of `range` not covered by any input interval, in order. */
    static List<Interval> gaps(List<Interval> input, Interval range) {
        List<Interval> out = new ArrayList<>();
        long cursor = range.start();
        for (Interval iv : merge(input)) {
            if (iv.end() <= range.start()) continue;
            if (iv.start() >= range.end()) break;
            if (iv.start() > cursor) out.add(new Interval(cursor, iv.start()));
            cursor = Math.max(cursor, iv.end());
        }
        if (cursor < range.end()) out.add(new Interval(cursor, range.end()));
        return out;
    }

    /**
     * Sweep line: turn every interval into a +1 event at its start and a -1 event at its end, sort events by
     * time with -1 before +1 at equal times (half-open: an interval ending at t does not overlap one starting
     * at t), and walk them keeping a running count. The maximum count is the maximum overlap. O(n log n).
     */
    static Peak maxOverlap(List<Interval> input) {
        List<long[]> events = new ArrayList<>(input.size() * 2);
        for (Interval iv : input) {
            if (iv.isEmpty()) continue;
            events.add(new long[] {iv.start(), +1});
            events.add(new long[] {iv.end(), -1});
        }
        events.sort(Comparator.<long[]>comparingLong(e -> e[0]).thenComparingLong(e -> e[1]));   // -1 sorts before +1
        int open = 0, best = 0;
        long bestAt = input.isEmpty() ? 0 : Long.MIN_VALUE;
        for (long[] e : events) {
            open += (int) e[1];
            if (open > best) {
                best = open;
                bestAt = e[0];
            }
        }
        return new Peak(best, best == 0 ? 0 : bestAt);
    }

    /**
     * Interval scheduling: the largest set of mutually non-overlapping intervals. Greedy by earliest end time,
     * proved optimal by an exchange argument (Phase 2, Lesson 07). O(n log n). Ties on end time are broken by
     * start time via the secondary sort key, so the result is deterministic.
     */
    static List<Interval> maxNonOverlapping(List<Interval> input) {
        List<Interval> byEnd = new ArrayList<>(input);
        byEnd.sort(Comparator.comparingLong(Interval::end).thenComparingLong(Interval::start));
        List<Interval> chosen = new ArrayList<>();
        long lastEnd = Long.MIN_VALUE;
        for (Interval iv : byEnd) {
            if (iv.isEmpty()) continue;
            if (iv.start() >= lastEnd) {
                chosen.add(iv);
                lastEnd = iv.end();
            }
        }
        return chosen;
    }

    /** All intervals containing instant t, in sorted order. Linear scan; a segment or interval tree does this in O(log n + k). */
    static List<Interval> stabbing(List<Interval> input, long t) {
        List<Interval> out = new ArrayList<>();
        for (Interval iv : input) if (iv.contains(t)) out.add(iv);
        Collections.sort(out);
        return out;
    }
}

/** A card authorization hold: an amount reserved on an account for a window of time. */
record Hold(String id, Money amount, Interval window) {
    Hold {
        Objects.requireNonNull(id, "id");
        if (amount.minorUnits() <= 0) throw new IllegalArgumentException("hold amount must be positive: " + amount);
    }
}

/**
 * All holds against one account. Answers the two questions a risk engine asks: how much is held right now,
 * and what was the worst-case simultaneous exposure over a period. The second is a weighted sweep line.
 */
final class HoldBook {
    private final Currency currency;
    private final List<Hold> holds = new ArrayList<>();

    HoldBook(Currency currency) { this.currency = Objects.requireNonNull(currency); }

    HoldBook add(Hold h) {
        if (!h.amount().currency().equals(currency)) {
            throw new IllegalArgumentException("hold in " + h.amount().currency().code() + " on a " + currency.code() + " book");
        }
        holds.add(h);
        return this;
    }

    List<Hold> holds() { return Collections.unmodifiableList(holds); }

    /** Sum of holds whose window contains t. */
    Money exposureAt(long t) {
        Money total = Money.zero(currency);
        for (Hold h : holds) if (h.window().contains(t)) total = total.plus(h.amount());
        return total;
    }

    /** Holds that have expired by t (window ended at or before t) without being captured: candidates to release. */
    List<Hold> expiredBy(long t) {
        List<Hold> out = new ArrayList<>();
        for (Hold h : holds) if (h.window().end() <= t) out.add(h);
        return out;
    }

    /**
     * Weighted sweep: +amount at each start, -amount at each end, releases before reservations at equal
     * instants. Returns the maximum simultaneous held amount and the first instant it was reached.
     */
    PeakExposure peakExposure() {
        List<long[]> events = new ArrayList<>(holds.size() * 2);
        for (int i = 0; i < holds.size(); i++) {
            Hold h = holds.get(i);
            if (h.window().isEmpty()) continue;
            events.add(new long[] {h.window().start(), +1, i});
            events.add(new long[] {h.window().end(), -1, i});
        }
        events.sort(Comparator.<long[]>comparingLong(e -> e[0]).thenComparingLong(e -> e[1]));
        Money open = Money.zero(currency), best = Money.zero(currency);
        long bestAt = 0;
        int openCount = 0, bestCount = 0;
        for (long[] e : events) {
            Money amt = holds.get((int) e[2]).amount();
            open = e[1] > 0 ? open.plus(amt) : open.minus(amt);
            openCount += (int) e[1];
            if (open.compareTo(best) > 0) {
                best = open;
                bestAt = e[0];
                bestCount = openCount;
            }
        }
        return new PeakExposure(best, bestAt, bestCount);
    }

    /** The union of all hold windows: when was anything at all held. */
    List<Interval> busyPeriods() {
        List<Interval> windows = new ArrayList<>();
        for (Hold h : holds) windows.add(h.window());
        return Intervals.merge(windows);
    }
}

record PeakExposure(Money amount, long at, int holdsOpen) {}

public final class Main {
    /** Minutes after midnight, so the numbers stay readable. */
    static long t(int hh, int mm) { return hh * 60L + mm; }

    static HoldBook sampleHolds() {
        Currency usd = Currency.USD;
        return new HoldBook(usd)
                .add(new Hold("hotel", Money.ofMinor(25_000, usd), new Interval(t(9, 0), t(17, 0))))
                .add(new Hold("fuel", Money.ofMinor(12_500, usd), new Interval(t(10, 30), t(11, 30))))
                .add(new Hold("restaurant", Money.ofMinor(8_000, usd), new Interval(t(12, 0), t(13, 0))))
                .add(new Hold("car-rental", Money.ofMinor(50_000, usd), new Interval(t(11, 0), t(15, 0))))
                .add(new Hold("parking", Money.ofMinor(1_500, usd), new Interval(t(13, 0), t(14, 0))))
                .add(new Hold("coffee", Money.ofMinor(600, usd), new Interval(t(17, 0), t(17, 15))));
    }

    public static void main(String[] args) {
        System.out.println("== Settlement windows: merge overlapping cutoffs ==");
        List<Interval> windows = List.of(
                new Interval(t(14, 0), t(15, 30)),   // scheme A cutoff window
                new Interval(t(15, 0), t(16, 0)),    // scheme B
                new Interval(t(16, 0), t(16, 45)),   // scheme C, touches B
                new Interval(t(9, 0), t(10, 0)),     // morning batch
                new Interval(t(18, 0), t(18, 0)));   // an empty placeholder
        System.out.println("input:  " + windows);
        System.out.println("merged: " + Intervals.merge(windows));
        System.out.println("coverage: " + Intervals.coverage(windows) + " minutes; gaps in 08:00-19:00: "
                + Intervals.gaps(windows, new Interval(t(8, 0), t(19, 0))));

        System.out.println();
        System.out.println("== Max overlap: how many windows are open at once ==");
        Peak p = Intervals.maxOverlap(windows);
        System.out.println("peak " + p.count() + " windows open, first reached at minute " + p.at() + " (" + p.at() / 60 + ":" + String.format("%02d", p.at() % 60) + ")");

        System.out.println();
        System.out.println("== Interval scheduling: most non-overlapping maintenance slots ==");
        List<Interval> slots = List.of(new Interval(1, 4), new Interval(3, 5), new Interval(0, 6), new Interval(5, 7),
                new Interval(3, 9), new Interval(5, 9), new Interval(6, 10), new Interval(8, 11), new Interval(8, 12),
                new Interval(2, 14), new Interval(12, 16));
        System.out.println("slots: " + slots);
        System.out.println("chosen by earliest end: " + Intervals.maxNonOverlapping(slots));

        System.out.println();
        System.out.println("== Authorization holds on one card: exposure over the day ==");
        HoldBook book = sampleHolds();
        for (Hold h : book.holds()) System.out.printf("  %-11s %12s  %s%n", h.id(), h.amount(), h.window());
        System.out.println("exposure at 12:30 = " + book.exposureAt(t(12, 30)));
        System.out.println("exposure at 17:00 = " + book.exposureAt(t(17, 0)) + "  (hotel released at 17:00, coffee starts: half-open)");
        PeakExposure peak = book.peakExposure();
        System.out.println("peak exposure " + peak.amount() + " with " + peak.holdsOpen() + " holds open, first at minute " + peak.at());
        System.out.println("busy periods: " + book.busyPeriods());
        System.out.println("expired by 14:00: " + book.expiredBy(t(14, 0)).stream().map(Hold::id).toList());
    }
}
