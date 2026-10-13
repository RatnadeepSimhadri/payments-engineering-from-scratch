// Lesson: phases/08-messaging-and-streams/08-watermarks-and-late-data/docs/en.md
// Watermarks, Event Time, and Late Data
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Java 21, standard library only. Semantics follow the Dataflow model (Akidau et al., VLDB 2015)
// and Apache Flink's bounded-out-of-orderness watermark strategy.

import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.TreeMap;

/** ISO 4217 currency: three-letter code plus minor-unit exponent. Copied from Phase 10, Lesson 01. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);

    Currency {
        if (code == null || code.length() != 3) throw new IllegalArgumentException("currency code must be three letters");
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4");
    }
}

/** Integer minor units plus a currency. Never a float. */
record Money(long minorUnits, Currency currency) {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    static Money zero(Currency currency) { return new Money(0, currency); }

    Money plus(Money other) {
        if (!currency.equals(other.currency)) {
            throw new IllegalArgumentException("cannot combine " + currency.code() + " with " + other.currency.code());
        }
        return new Money(Math.addExact(minorUnits, other.minorUnits), currency);
    }

    @Override public String toString() {
        long scale = 1;
        for (int i = 0; i < currency.exponent(); i++) scale *= 10;
        long abs = Math.abs(minorUnits);
        String frac = currency.exponent() == 0 ? "" : "." + String.format("%0" + currency.exponent() + "d", abs % scale);
        return (minorUnits < 0 ? "-" : "") + (abs / scale) + frac + " " + currency.code();
    }
}

/**
 * A payment event. eventTime is when the payment happened (stamped by the producer);
 * arrivalTime is when the stream processor saw it (stamped by the processor's clock).
 * Both are epoch-style milliseconds; the demo uses minutes-since-midnight for readability.
 */
record Event(String key, long eventTime, long arrivalTime, Money amount) {}

/** Why an event landed in the side output. */
enum LateReason { WINDOW_CLOSED }

/** A late event parked in the side output, with the window it would have belonged to. */
record LateEvent(Event event, long windowStart, long windowEnd, long watermarkAtArrival, LateReason reason) {}

/** Whether a window result is its first (on-time) firing or a corrected re-firing caused by an allowed-late event. */
enum FiringKind { ON_TIME, LATE_UPDATE }

/** A window result: [windowStart, windowEnd) is half-open. */
record WindowResult(long windowStart, long windowEnd, int count, Money sum, FiringKind kind) {}

/** Half-open interval [start, end). Tumbling windows of a fixed size tile the time axis. */
record Window(long start, long end) implements Comparable<Window> {
    @Override public int compareTo(Window o) { return Long.compare(start, o.start); }
}

/** Assigns each event time to exactly one tumbling window: start = floor(t / size) * size. */
final class TumblingWindowAssigner {
    private final long sizeMillis;

    TumblingWindowAssigner(long sizeMillis) {
        if (sizeMillis <= 0) throw new IllegalArgumentException("window size must be positive");
        this.sizeMillis = sizeMillis;
    }

    Window assign(long timestamp) {
        long start = Math.floorDiv(timestamp, sizeMillis) * sizeMillis;
        return new Window(start, start + sizeMillis);
    }
}

/**
 * Bounded out-of-orderness watermark. After seeing a maximum event time T, the generator asserts
 * that no further event with eventTime < T - maxOutOfOrderness will arrive. The watermark never
 * moves backwards: a late event does not pull it down.
 */
final class BoundedOutOfOrdernessWatermark {
    private final long maxOutOfOrdernessMillis;
    private long maxEventTime = Long.MIN_VALUE;

    BoundedOutOfOrdernessWatermark(long maxOutOfOrdernessMillis) {
        if (maxOutOfOrdernessMillis < 0) throw new IllegalArgumentException("out-of-orderness must be >= 0");
        this.maxOutOfOrdernessMillis = maxOutOfOrdernessMillis;
    }

    /** Observe an event time; returns the (possibly unchanged) current watermark. */
    long observe(long eventTime) {
        if (eventTime > maxEventTime) maxEventTime = eventTime;
        return current();
    }

    /** Long.MIN_VALUE before any event has been seen: nothing is known to be complete. */
    long current() {
        if (maxEventTime == Long.MIN_VALUE) return Long.MIN_VALUE;
        return maxEventTime - maxOutOfOrdernessMillis;
    }
}

/** Mutable per-window accumulator. Kept until the window is garbage-collected after allowed lateness. */
final class WindowState {
    int count = 0;
    Money sum;
    boolean fired = false;

    WindowState(Currency currency) { sum = Money.zero(currency); }

    void add(Event e) {
        count++;
        sum = sum.plus(e.amount());
    }
}

/**
 * Event-time tumbling-window operator with a watermark, allowed lateness, and a late-data side output.
 *
 * Invariants:
 *  1. A window [s, e) fires ON_TIME exactly once, when the watermark first reaches e.
 *  2. An event with eventTime < watermark whose window has already fired is either folded in as a
 *     LATE_UPDATE (if watermark < e + allowedLateness) or sent to the side output, never dropped silently.
 *  3. Every event is accounted for exactly once: sum over all final window counts + side output size == events seen.
 */
final class EventTimeWindowOperator {
    private final TumblingWindowAssigner assigner;
    private final BoundedOutOfOrdernessWatermark watermark;
    private final long allowedLatenessMillis;
    private final Currency currency;
    private final TreeMap<Window, WindowState> open = new TreeMap<>();
    private final List<LateEvent> sideOutput = new ArrayList<>();
    private final List<WindowResult> emitted = new ArrayList<>();
    private long eventsSeen = 0;
    private long eventsInClosedWindows = 0;

    EventTimeWindowOperator(long windowSizeMillis, long maxOutOfOrdernessMillis, long allowedLatenessMillis, Currency currency) {
        if (allowedLatenessMillis < 0) throw new IllegalArgumentException("allowed lateness must be >= 0");
        this.assigner = new TumblingWindowAssigner(windowSizeMillis);
        this.watermark = new BoundedOutOfOrdernessWatermark(maxOutOfOrdernessMillis);
        this.allowedLatenessMillis = allowedLatenessMillis;
        this.currency = currency;
    }

    /** Process one event; returns the results emitted as a consequence (zero, one, or several). */
    List<WindowResult> process(Event e) {
        eventsSeen++;
        List<WindowResult> out = new ArrayList<>();
        Window w = assigner.assign(e.eventTime());
        long wmBefore = watermark.current();

        if (wmBefore != Long.MIN_VALUE && wmBefore >= w.end() + allowedLatenessMillis) {
            // The window was fired and then garbage-collected. Side output, never silent drop.
            sideOutput.add(new LateEvent(e, w.start(), w.end(), wmBefore, LateReason.WINDOW_CLOSED));
            watermark.observe(e.eventTime());
            return out;
        }

        WindowState state = open.computeIfAbsent(w, k -> new WindowState(currency));
        state.add(e);
        if (state.fired) {
            // Window already fired on time; this is an allowed-late event. Re-emit a corrected result.
            out.add(new WindowResult(w.start(), w.end(), state.count, state.sum, FiringKind.LATE_UPDATE));
        }

        long wmAfter = watermark.observe(e.eventTime());
        out.addAll(advance(wmAfter));
        return out;
    }

    /** Fire every unfired window whose end <= watermark, then GC windows past end + allowed lateness. */
    private List<WindowResult> advance(long wm) {
        List<WindowResult> out = new ArrayList<>();
        if (wm == Long.MIN_VALUE) return out;
        for (Map.Entry<Window, WindowState> entry : open.entrySet()) {
            Window w = entry.getKey();
            if (w.end() > wm) break;                      // TreeMap is ordered by start; later windows are not ready
            WindowState s = entry.getValue();
            if (!s.fired) {
                s.fired = true;
                out.add(new WindowResult(w.start(), w.end(), s.count, s.sum, FiringKind.ON_TIME));
            }
        }
        var it = open.entrySet().iterator();
        while (it.hasNext()) {
            var entry = it.next();
            if (wm >= entry.getKey().end() + allowedLatenessMillis) {
                eventsInClosedWindows += entry.getValue().count;
                it.remove();
            } else {
                break;
            }
        }
        emitted.addAll(out);
        return out;
    }

    long currentWatermark() { return watermark.current(); }

    List<LateEvent> sideOutput() { return List.copyOf(sideOutput); }

    List<WindowResult> allEmitted() { return List.copyOf(emitted); }

    int openWindowCount() { return open.size(); }

    /** Invariant 3: nothing lost, nothing double-counted. */
    boolean accountsForEveryEvent() {
        long inOpen = open.values().stream().mapToLong(s -> s.count).sum();
        return eventsSeen == inOpen + eventsInClosedWindows + sideOutput.size();
    }
}

/**
 * The naive alternative: windows keyed by arrival (processing) time. Deterministic here because the
 * processor's clock is injected via Event.arrivalTime. It never has late data, because it never asks
 * when anything actually happened.
 */
final class ProcessingTimeWindowOperator {
    private final TumblingWindowAssigner assigner;
    private final TreeMap<Window, WindowState> windows = new TreeMap<>();
    private final Currency currency;

    ProcessingTimeWindowOperator(long windowSizeMillis, Currency currency) {
        this.assigner = new TumblingWindowAssigner(windowSizeMillis);
        this.currency = currency;
    }

    void process(Event e) {
        windows.computeIfAbsent(assigner.assign(e.arrivalTime()), k -> new WindowState(currency)).add(e);
    }

    List<WindowResult> results() {
        List<WindowResult> out = new ArrayList<>();
        windows.forEach((w, s) -> out.add(new WindowResult(w.start(), w.end(), s.count, s.sum, FiringKind.ON_TIME)));
        return out;
    }
}

public final class Main {
    static final long MINUTE = 60_000L;

    /** "11:59" -> milliseconds since midnight. Keeps the demo readable. */
    static long t(String hhmm) {
        String[] p = hhmm.split(":");
        return (Long.parseLong(p[0]) * 60 + Long.parseLong(p[1])) * MINUTE;
    }

    static String hhmm(long millis) {
        if (millis == Long.MIN_VALUE) return "-inf";
        long minutes = Math.floorDiv(millis, MINUTE);
        return String.format("%02d:%02d", Math.floorDiv(minutes, 60), Math.floorMod(minutes, 60));
    }

    static String show(WindowResult r) {
        return "[" + hhmm(r.windowStart()) + ", " + hhmm(r.windowEnd()) + ") count=" + r.count()
                + " sum=" + r.sum() + " " + r.kind();
    }

    static Event ev(String key, String eventTime, String arrivalTime, long cents) {
        return new Event(key, t(eventTime), t(arrivalTime), Money.ofMinor(cents, Currency.USD));
    }

    public static void main(String[] args) {
        // Hourly settlement totals per merchant. The 11:59 authorization from a store with a flaky
        // uplink arrives at 12:03, after the 12:00-12:05 traffic has already been seen.
        List<Event> events = List.of(
                ev("m-1", "11:40", "11:40", 1999),
                ev("m-1", "11:52", "11:52", 4500),
                ev("m-2", "11:58", "11:58", 1250),
                ev("m-1", "12:01", "12:01", 800),
                ev("m-2", "12:02", "12:02", 3000),
                ev("m-1", "11:59", "12:03", 7700),   // happened in the 11:00 hour, arrived in the 12:00 hour
                ev("m-2", "12:04", "12:04", 1500),
                ev("m-1", "12:09", "12:09", 2000),   // watermark reaches 12:04: the 11:00 window fires
                ev("m-2", "11:57", "12:10", 999),    // late but within allowed lateness: corrected re-firing
                ev("m-1", "12:16", "12:16", 600),    // watermark 12:11 passes 12:00 + 10 min: 11:00 window is GC'd
                ev("m-2", "11:50", "12:20", 2500),   // too late: side output
                ev("m-1", "13:06", "13:06", 100)     // watermark 13:01 fires the 12:00 window
        );

        System.out.println("== Processing-time windows: the 11:59 payment lands in the 12:00 hour ==");
        var pt = new ProcessingTimeWindowOperator(60 * MINUTE, Currency.USD);
        events.forEach(pt::process);
        pt.results().forEach(r -> System.out.println("  " + show(r)));

        System.out.println();
        System.out.println("== Event-time windows, watermark = max event time - 5 min, allowed lateness 10 min ==");
        var op = new EventTimeWindowOperator(60 * MINUTE, 5 * MINUTE, 10 * MINUTE, Currency.USD);
        for (Event e : events) {
            List<WindowResult> fired = op.process(e);
            System.out.printf("  event %s @%s (arrived %s) %-12s watermark=%s%n",
                    e.key(), hhmm(e.eventTime()), hhmm(e.arrivalTime()), e.amount(), hhmm(op.currentWatermark()));
            fired.forEach(r -> System.out.println("    -> fired " + show(r)));
        }

        System.out.println();
        System.out.println("== Side output (late data that missed the window entirely) ==");
        for (LateEvent le : op.sideOutput()) {
            System.out.printf("  %s @%s arrived %s amount=%s target=[%s, %s) watermark-at-arrival=%s reason=%s%n",
                    le.event().key(), hhmm(le.event().eventTime()), hhmm(le.event().arrivalTime()), le.event().amount(),
                    hhmm(le.windowStart()), hhmm(le.windowEnd()), hhmm(le.watermarkAtArrival()), le.reason());
        }
        System.out.println();
        System.out.println("every event accounted for exactly once: " + op.accountsForEveryEvent());
        System.out.println("open windows still holding state: " + op.openWindowCount());
    }
}
