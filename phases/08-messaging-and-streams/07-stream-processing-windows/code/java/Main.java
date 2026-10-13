// Lesson: phases/08-messaging-and-streams/07-stream-processing-windows/docs/en.md
// Stream Processing: Tumbling, Sliding, and Session Windows
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Akidau et al., "The Dataflow Model" (VLDB 2015), sections 2.2-2.3 (windowing, triggers);
//          Apache Flink documentation, "Windows" (assigners, triggers, state); Kafka Streams "Windowing" docs.
// Java 21, standard library only. Time is the record's processing timestamp plus an explicit timer; nothing sleeps.

import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.Iterator;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.TreeMap;

/** ISO 4217 currency: code and minor-unit exponent. Copied from Phase 10, Lesson 01; lessons are self-contained. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);

    Currency {
        if (code == null || code.length() != 3) throw new IllegalArgumentException("currency code must be three letters: " + code);
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4: " + exponent);
    }
}

final class CurrencyMismatchException extends RuntimeException {
    CurrencyMismatchException(Currency a, Currency b) { super("cannot combine " + a.code() + " with " + b.code()); }
}

/** Integer minor units plus a currency. Addition is checked; mixing currencies is an error. */
record Money(long minorUnits, Currency currency) implements Comparable<Money> {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    static Money zero(Currency currency) { return new Money(0, currency); }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
    }

    Money plus(Money other) {
        requireSameCurrency(other);
        return new Money(Math.addExact(minorUnits, other.minorUnits), currency);
    }

    Money max(Money other) { return compareTo(other) >= 0 ? this : other; }

    @Override
    public int compareTo(Money other) {
        requireSameCurrency(other);
        return Long.compare(minorUnits, other.minorUnits);
    }

    @Override
    public String toString() {
        long scale = 1;
        for (int i = 0; i < currency.exponent(); i++) scale *= 10;
        long abs = Math.abs(minorUnits);
        String frac = Long.toString(abs % scale);
        while (frac.length() < currency.exponent()) frac = "0" + frac;
        return (minorUnits < 0 ? "-" : "") + (abs / scale) + "." + frac + " " + currency.code();
    }
}

// ---------------------------------------------------------------------------------------------------------------------
// Records, windows, aggregates.
// ---------------------------------------------------------------------------------------------------------------------

/**
 * One card transaction as the stream processor sees it. {@code time} is the processing time: the clock when the record
 * arrived. This lesson uses processing time only; Lesson 08 separates event time from it.
 */
record Txn(String cardId, Money amount, long time) {
    Txn {
        Objects.requireNonNull(cardId, "cardId");
        if (time < 0) throw new IllegalArgumentException("time must be non-negative");
    }
}

/** A half-open time interval [start, end). Ordered by start, then end, so windows sort into a TreeMap. */
record Window(long start, long end) implements Comparable<Window> {
    Window {
        if (end <= start) throw new IllegalArgumentException("window must have positive length: [" + start + ", " + end + ")");
    }

    boolean contains(long t) { return t >= start && t < end; }

    long length() { return end - start; }

    @Override
    public int compareTo(Window o) { return start != o.start ? Long.compare(start, o.start) : Long.compare(end, o.end); }

    @Override
    public String toString() { return "[" + start + ", " + end + ")"; }
}

/** What is kept per (key, window): count, sum, max. Updated in O(1) per record; the records themselves are not kept. */
record Aggregate(int count, Money sum, Money max) {
    static Aggregate empty(Currency currency) { return new Aggregate(0, Money.zero(currency), Money.zero(currency)); }

    Aggregate add(Money amount) { return new Aggregate(count + 1, sum.plus(amount), max.max(amount)); }

    Aggregate merge(Aggregate other) { return new Aggregate(count + other.count, sum.plus(other.sum), max.max(other.max)); }
}

/** An emitted result: one key, one closed window, its aggregate, and the processing time at which it was emitted. */
record WindowResult(String key, Window window, Aggregate aggregate, long emittedAt) {}

// ---------------------------------------------------------------------------------------------------------------------
// Assigners: a pure function from a timestamp to the windows it belongs to.
// ---------------------------------------------------------------------------------------------------------------------

interface WindowAssigner {
    List<Window> assign(long time);

    String describe();
}

/** Fixed-size, non-overlapping windows aligned to the epoch: every timestamp is in exactly one. */
final class TumblingWindows implements WindowAssigner {
    final long size;

    TumblingWindows(long size) {
        if (size <= 0) throw new IllegalArgumentException("size must be positive");
        this.size = size;
    }

    @Override
    public List<Window> assign(long time) {
        long start = time - Math.floorMod(time, size);
        return List.of(new Window(start, start + size));
    }

    @Override
    public String describe() { return "tumbling(size=" + size + ")"; }
}

/** Fixed-size windows that start every {@code slide}: every timestamp is in size/slide windows. */
final class SlidingWindows implements WindowAssigner {
    final long size, slide;

    SlidingWindows(long size, long slide) {
        if (size <= 0 || slide <= 0) throw new IllegalArgumentException("size and slide must be positive");
        if (slide > size) throw new IllegalArgumentException("slide larger than size leaves gaps; use tumbling windows");
        if (size % slide != 0) throw new IllegalArgumentException("size must be a multiple of slide");
        this.size = size;
        this.slide = slide;
    }

    @Override
    public List<Window> assign(long time) {
        List<Window> out = new ArrayList<>();
        long lastStart = time - Math.floorMod(time, slide);
        for (long start = lastStart; start > time - size; start -= slide) {
            out.add(new Window(start, start + size));
        }
        Collections.reverse(out);
        return out;
    }

    @Override
    public String describe() { return "sliding(size=" + size + ", slide=" + slide + ")"; }
}

// ---------------------------------------------------------------------------------------------------------------------
// Operators. Each keeps per-key, per-window state, updates it on process(), and emits on onTimer(now) when a window's
// trigger condition is met in processing time.
// ---------------------------------------------------------------------------------------------------------------------

interface WindowOperator {
    void process(Txn txn);

    List<WindowResult> onTimer(long now);

    int stateSize();

    String describe();
}

/** Tumbling or sliding windows: assigner decides membership, the trigger is "processing time has passed the window end". */
final class WindowedAggregator implements WindowOperator {
    private final WindowAssigner assigner;
    private final Currency currency;
    private final Map<String, TreeMap<Window, Aggregate>> state = new HashMap<>();
    private long maxTimeSeen = -1;

    WindowedAggregator(WindowAssigner assigner, Currency currency) {
        this.assigner = assigner;
        this.currency = currency;
    }

    @Override
    public void process(Txn txn) {
        if (txn.time() < maxTimeSeen) {
            throw new IllegalArgumentException("processing time went backwards: " + txn.time() + " < " + maxTimeSeen);
        }
        maxTimeSeen = txn.time();
        TreeMap<Window, Aggregate> perKey = state.computeIfAbsent(txn.cardId(), k -> new TreeMap<>());
        for (Window w : assigner.assign(txn.time())) {
            perKey.merge(w, Aggregate.empty(currency).add(txn.amount()), Aggregate::merge);
        }
    }

    /** Emit and drop every window whose end <= now. Windows are emitted once; later records cannot reach them. */
    @Override
    public List<WindowResult> onTimer(long now) {
        List<WindowResult> out = new ArrayList<>();
        Iterator<Map.Entry<String, TreeMap<Window, Aggregate>>> keys = state.entrySet().iterator();
        while (keys.hasNext()) {
            Map.Entry<String, TreeMap<Window, Aggregate>> entry = keys.next();
            TreeMap<Window, Aggregate> windows = entry.getValue();
            while (!windows.isEmpty() && windows.firstKey().end() <= now) {
                Map.Entry<Window, Aggregate> closed = windows.pollFirstEntry();
                out.add(new WindowResult(entry.getKey(), closed.getKey(), closed.getValue(), now));
            }
            if (windows.isEmpty()) keys.remove();
        }
        out.sort((a, b) -> a.window().compareTo(b.window()) != 0 ? a.window().compareTo(b.window()) : a.key().compareTo(b.key()));
        return out;
    }

    /** Current (key, window) aggregate, or null. Used by tests and by the demo to show state before a trigger. */
    Aggregate peek(String key, Window window) {
        TreeMap<Window, Aggregate> perKey = state.get(key);
        return perKey == null ? null : perKey.get(window);
    }

    @Override
    public int stateSize() {
        int n = 0;
        for (TreeMap<Window, Aggregate> m : state.values()) n += m.size();
        return n;
    }

    @Override
    public String describe() { return assigner.describe(); }
}

/**
 * Session windows: per key, a record extends the session it falls within {@code gap} of, or starts a new one. Two
 * sessions that a record bridges are merged. A session closes when processing time passes its last record + gap.
 */
final class SessionAggregator implements WindowOperator {
    final long gap;
    private final Currency currency;
    private final Map<String, TreeMap<Window, Aggregate>> state = new HashMap<>();
    private long maxTimeSeen = -1;

    SessionAggregator(long gap, Currency currency) {
        if (gap <= 0) throw new IllegalArgumentException("gap must be positive");
        this.gap = gap;
        this.currency = currency;
    }

    @Override
    public void process(Txn txn) {
        if (txn.time() < maxTimeSeen) {
            throw new IllegalArgumentException("processing time went backwards: " + txn.time() + " < " + maxTimeSeen);
        }
        maxTimeSeen = txn.time();
        TreeMap<Window, Aggregate> sessions = state.computeIfAbsent(txn.cardId(), k -> new TreeMap<>());
        // A record's own session is [t, t + gap). Merge with every existing session it overlaps.
        Window mine = new Window(txn.time(), txn.time() + gap);
        Aggregate agg = Aggregate.empty(currency).add(txn.amount());
        Iterator<Map.Entry<Window, Aggregate>> it = sessions.entrySet().iterator();
        while (it.hasNext()) {
            Map.Entry<Window, Aggregate> e = it.next();
            Window w = e.getKey();
            boolean overlaps = w.start() < mine.end() && mine.start() < w.end();
            if (overlaps) {
                mine = new Window(Math.min(w.start(), mine.start()), Math.max(w.end(), mine.end()));
                agg = agg.merge(e.getValue());
                it.remove();
            }
        }
        sessions.put(mine, agg);
    }

    /** A session [start, end) with end = lastRecord + gap closes when now >= end: the gap has elapsed with nothing new. */
    @Override
    public List<WindowResult> onTimer(long now) {
        List<WindowResult> out = new ArrayList<>();
        Iterator<Map.Entry<String, TreeMap<Window, Aggregate>>> keys = state.entrySet().iterator();
        while (keys.hasNext()) {
            Map.Entry<String, TreeMap<Window, Aggregate>> entry = keys.next();
            TreeMap<Window, Aggregate> sessions = entry.getValue();
            Iterator<Map.Entry<Window, Aggregate>> it = sessions.entrySet().iterator();
            while (it.hasNext()) {
                Map.Entry<Window, Aggregate> e = it.next();
                if (e.getKey().end() <= now) {
                    // Report the session as [first record, last record + gap); the gap is part of the window by definition.
                    out.add(new WindowResult(entry.getKey(), e.getKey(), e.getValue(), now));
                    it.remove();
                }
            }
            if (sessions.isEmpty()) keys.remove();
        }
        out.sort((a, b) -> a.window().compareTo(b.window()) != 0 ? a.window().compareTo(b.window()) : a.key().compareTo(b.key()));
        return out;
    }

    List<Window> openSessions(String key) { return new ArrayList<>(state.getOrDefault(key, new TreeMap<>()).keySet()); }

    @Override
    public int stateSize() {
        int n = 0;
        for (TreeMap<Window, Aggregate> m : state.values()) n += m.size();
        return n;
    }

    @Override
    public String describe() { return "session(gap=" + gap + ")"; }
}

// ---------------------------------------------------------------------------------------------------------------------
// Driver and the velocity rule.
// ---------------------------------------------------------------------------------------------------------------------

/** Feeds records to an operator in time order and fires the timer every {@code timerInterval} of processing time. */
final class StreamRunner {
    private final WindowOperator operator;
    private final long timerInterval;
    private long nextTimer;
    private int maxStateSize = 0;
    private final List<WindowResult> results = new ArrayList<>();

    StreamRunner(WindowOperator operator, long timerInterval) {
        if (timerInterval <= 0) throw new IllegalArgumentException("timerInterval must be positive");
        this.operator = operator;
        this.timerInterval = timerInterval;
        this.nextTimer = timerInterval;
    }

    /** Process all records (must be in non-decreasing time), then fire the timer at {@code endOfStream}. */
    List<WindowResult> run(List<Txn> txns, long endOfStream) {
        for (Txn t : txns) {
            while (nextTimer <= t.time()) {
                results.addAll(operator.onTimer(nextTimer));
                nextTimer += timerInterval;
            }
            operator.process(t);
            maxStateSize = Math.max(maxStateSize, operator.stateSize());
        }
        while (nextTimer <= endOfStream) {
            results.addAll(operator.onTimer(nextTimer));
            nextTimer += timerInterval;
        }
        return Collections.unmodifiableList(results);
    }

    int maxStateSize() { return maxStateSize; }
}

/** The fraud rule: more than {@code maxPerWindow} transactions for one card in one window is a velocity breach. */
final class VelocityRule {
    final int maxPerWindow;

    VelocityRule(int maxPerWindow) { this.maxPerWindow = maxPerWindow; }

    List<WindowResult> breaches(List<WindowResult> results) {
        List<WindowResult> out = new ArrayList<>();
        for (WindowResult r : results) if (r.aggregate().count() > maxPerWindow) out.add(r);
        return out;
    }
}

public final class Main {
    static final long SEC = 1_000;
    static final long MIN = 60 * SEC;

    static Txn txn(String card, long cents, long time) { return new Txn(card, Money.ofMinor(cents, Currency.USD), time); }

    /** The demo stream: three cards over about four minutes of processing time. */
    static List<Txn> demoStream() {
        List<Txn> s = new ArrayList<>();
        // card-A: four quick transactions straddling the 1:00 boundary, then quiet
        s.add(txn("card-A", 1_299, 0 * MIN + 50 * SEC));
        s.add(txn("card-A", 2_450, 0 * MIN + 56 * SEC));
        s.add(txn("card-A", 899, 1 * MIN + 3 * SEC));
        s.add(txn("card-A", 15_000, 1 * MIN + 9 * SEC));
        // card-B: one transaction a minute, steady
        for (int m = 0; m < 4; m++) s.add(txn("card-B", 4_000 + m, m * MIN + 30 * SEC));
        // card-C: a burst at 2:00-2:10, a pause, another burst at 3:00
        s.add(txn("card-C", 500, 2 * MIN + 0 * SEC));
        s.add(txn("card-C", 500, 2 * MIN + 4 * SEC));
        s.add(txn("card-C", 500, 2 * MIN + 10 * SEC));
        s.add(txn("card-C", 9_900, 3 * MIN + 0 * SEC));
        s.add(txn("card-C", 9_900, 3 * MIN + 5 * SEC));
        s.sort((a, b) -> Long.compare(a.time(), b.time()));
        return s;
    }

    static String mmss(long millis) { return String.format("%d:%02d", millis / MIN, (millis % MIN) / SEC); }

    static void print(List<WindowResult> results) {
        for (WindowResult r : results) {
            System.out.printf("  %-7s %s-%s  count=%d sum=%s max=%s  (emitted at %s)%n", r.key(), mmss(r.window().start()), mmss(r.window().end()),
                    r.aggregate().count(), r.aggregate().sum(), r.aggregate().max(), mmss(r.emittedAt()));
        }
    }

    public static void main(String[] args) {
        List<Txn> stream = demoStream();
        long end = 5 * MIN;
        VelocityRule rule = new VelocityRule(3);

        System.out.println("== 1. Window assignment is a pure function of the timestamp ==");
        long t = 1 * MIN + 3 * SEC;
        System.out.printf("t=%s  tumbling(60s): %s%n", mmss(t), new TumblingWindows(MIN).assign(t));
        System.out.printf("t=%s  sliding(60s, 10s): %s%n", mmss(t), new SlidingWindows(MIN, 10 * SEC).assign(t));

        System.out.println("\n== 2. Tumbling 1-minute windows: transactions per card per minute ==");
        StreamRunner tumbling = new StreamRunner(new WindowedAggregator(new TumblingWindows(MIN), Currency.USD), 10 * SEC);
        List<WindowResult> tumblingResults = tumbling.run(stream, end);
        print(tumblingResults);
        System.out.printf("velocity breaches (> 3 per window): %d   max state entries: %d%n", rule.breaches(tumblingResults).size(), tumbling.maxStateSize());
        System.out.println("card-A made 4 transactions in 19 seconds (0:50 to 1:09) and no tumbling window saw more than 2.");

        System.out.println("\n== 3. Sliding 1-minute windows every 10 seconds: the boundary problem goes away ==");
        StreamRunner sliding = new StreamRunner(new WindowedAggregator(new SlidingWindows(MIN, 10 * SEC), Currency.USD), 10 * SEC);
        List<WindowResult> slidingResults = sliding.run(stream, end);
        List<WindowResult> breaches = rule.breaches(slidingResults);
        System.out.printf("%d window results emitted (%d for card-A); breaches:%n", slidingResults.size(), slidingResults.stream().filter(r -> r.key().equals("card-A")).count());
        print(breaches);
        System.out.printf("max state entries: %d (each record lives in size/slide = 6 windows)%n", sliding.maxStateSize());

        System.out.println("\n== 4. Session windows with a 30-second gap: activity bursts, not clock minutes ==");
        SessionAggregator sessions = new SessionAggregator(30 * SEC, Currency.USD);
        StreamRunner sessionRunner = new StreamRunner(sessions, 10 * SEC);
        List<WindowResult> sessionResults = sessionRunner.run(stream, end);
        print(sessionResults);
        System.out.println("card-B never pauses less than 30 s between transactions, so each transaction is its own session;");
        System.out.println("card-C's two bursts are two sessions; card-A's four transactions are one.");

        System.out.println("\n== 5. Trigger timing: a window closes only when the timer passes its end ==");
        WindowedAggregator agg = new WindowedAggregator(new TumblingWindows(MIN), Currency.USD);
        agg.process(txn("card-A", 1_299, 50 * SEC));
        agg.process(txn("card-A", 2_450, 56 * SEC));
        Window first = new Window(0, MIN);
        System.out.printf("state for card-A %s before the timer: %s%n", first, agg.peek("card-A", first));
        System.out.printf("onTimer(0:59) emits %d windows; onTimer(1:00) emits %d%n", agg.onTimer(59 * SEC).size(), agg.onTimer(MIN).size());
        try {
            agg.process(txn("card-A", 1, 30 * SEC));
        } catch (IllegalArgumentException e) {
            System.out.println("a record with an earlier processing time is rejected: " + e.getMessage());
        }

        System.out.println("\n== 6. Session extension in detail ==");
        SessionAggregator s2 = new SessionAggregator(30 * SEC, Currency.USD);
        s2.process(txn("card-C", 500, 0));
        System.out.println("record at 0:00, gap 30 s -> open session " + s2.openSessions("card-C").stream().map(w -> mmss(w.start()) + "-" + mmss(w.end())).toList());
        s2.process(txn("card-C", 500, 25 * SEC));
        s2.process(txn("card-C", 500, 50 * SEC));
        System.out.println("records at 0:25 and 0:50 each arrive before the gap elapses -> extended to " + s2.openSessions("card-C").stream().map(w -> mmss(w.start()) + "-" + mmss(w.end())).toList());
        s2.process(txn("card-C", 500, 2 * MIN));
        System.out.println("record at 2:00 arrives after the gap -> two sessions: " + s2.openSessions("card-C").stream().map(w -> mmss(w.start()) + "-" + mmss(w.end())).toList());
        System.out.println("onTimer(1:20) closes the first: " + s2.onTimer(80 * SEC).stream().map(r -> mmss(r.window().start()) + "-" + mmss(r.window().end()) + " count=" + r.aggregate().count()).toList());
        System.out.printf("state entries: tumbling=%d sliding=%d session=%d (peak during the run)%n", tumbling.maxStateSize(), sliding.maxStateSize(), sessionRunner.maxStateSize());
    }
}
