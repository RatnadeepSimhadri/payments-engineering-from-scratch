// Lesson: phases/03-systems-data-structures/12-time-wheels-and-timers/docs/en.md
// Hierarchical Timing Wheels
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Varghese & Lauck, "Hashed and Hierarchical Timing Wheels" (SOSP 1987); Apache Kafka
// TimingWheel / SystemTimer (purgatory); Netty HashedWheelTimer. Java 21, standard library only.

import java.util.ArrayList;
import java.util.List;
import java.util.Objects;
import java.util.PriorityQueue;
import java.util.Random;
import java.util.function.Consumer;

/** Injected time source. Tests and the demo use ManualClock; production would read System.nanoTime(). */
interface Clock {
    long nowMillis();
}

final class ManualClock implements Clock {
    private long now;

    ManualClock(long startMillis) { this.now = startMillis; }

    @Override public long nowMillis() { return now; }

    void advance(long millis) {
        if (millis < 0) throw new IllegalArgumentException("time does not go backwards");
        now += millis;
    }
}

/**
 * A scheduled callback. Doubly linked so cancellation unlinks in O(1) without searching the bucket.
 * deadlineMillis is the tick boundary the entry fires on (the requested time rounded up to the wheel's tick).
 */
final class TimerEntry {
    final long id;
    final long deadlineMillis;
    final Runnable task;
    TimerEntry prev, next;
    Bucket bucket;
    private boolean cancelled, fired;

    TimerEntry(long id, long deadlineMillis, Runnable task) {
        this.id = id;
        this.deadlineMillis = deadlineMillis;
        this.task = Objects.requireNonNull(task, "task");
    }

    /** O(1): unlink from whatever bucket holds this entry. Returns false if already cancelled or already fired. */
    boolean cancel() {
        if (cancelled || fired) return false;
        cancelled = true;
        if (bucket != null) bucket.remove(this);
        return true;
    }

    /** Run the task once. Returns false if the entry was cancelled or has already fired. */
    boolean fire() {
        if (cancelled || fired) return false;
        fired = true;
        task.run();
        return true;
    }

    boolean isCancelled() { return cancelled; }
    boolean hasFired() { return fired; }
}

/** One slot of a wheel: a circular doubly linked list with a sentinel. add/remove are O(1); flush is O(k). */
final class Bucket {
    private final TimerEntry sentinel = new TimerEntry(-1, Long.MIN_VALUE, () -> {});
    private int size;

    Bucket() {
        sentinel.prev = sentinel;
        sentinel.next = sentinel;
    }

    void add(TimerEntry e) {
        e.bucket = this;
        e.prev = sentinel.prev;
        e.next = sentinel;
        sentinel.prev.next = e;
        sentinel.prev = e;
        size++;
    }

    void remove(TimerEntry e) {
        if (e.bucket != this) return;
        e.prev.next = e.next;
        e.next.prev = e.prev;
        e.prev = e.next = null;
        e.bucket = null;
        size--;
    }

    /** Remove every entry and hand each to the consumer, in insertion order. */
    void flush(Consumer<TimerEntry> consumer) {
        TimerEntry e = sentinel.next;
        while (e != sentinel) {
            TimerEntry next = e.next;
            remove(e);
            consumer.accept(e);
            e = next;
        }
    }

    int size() { return size; }
}

/**
 * One level of the hierarchy. A wheel of wheelSize buckets, each spanning tickMillis, so the wheel covers
 * interval = tickMillis * wheelSize. Deadlines beyond the interval go to a lazily created overflow wheel whose
 * tick is this wheel's whole interval. currentTime is always a multiple of tickMillis.
 */
final class TimingWheel {
    final long tickMillis;
    final int wheelSize;
    final long interval;
    private long currentTime;
    private final Bucket[] buckets;
    private TimingWheel overflow;
    int cascades;                                 // entries moved down from this wheel's overflow

    TimingWheel(long tickMillis, int wheelSize, long startMillis) {
        if (tickMillis <= 0 || wheelSize <= 1) throw new IllegalArgumentException("tick must be > 0 and wheelSize > 1");
        this.tickMillis = tickMillis;
        this.wheelSize = wheelSize;
        this.interval = Math.multiplyExact(tickMillis, (long) wheelSize);
        this.currentTime = startMillis - Math.floorMod(startMillis, tickMillis);
        this.buckets = new Bucket[wheelSize];
        for (int i = 0; i < wheelSize; i++) buckets[i] = new Bucket();
    }

    long currentTime() { return currentTime; }

    int levels() { return overflow == null ? 1 : 1 + overflow.levels(); }

    /**
     * Place an entry. Returns false if its deadline falls inside the current tick (already due): the caller
     * runs it. Otherwise it lands in this wheel's bucket or recursively in an overflow wheel.
     */
    boolean add(TimerEntry e) {
        long deadline = e.deadlineMillis;
        if (e.isCancelled()) return true;                            // nothing to do, nothing to run
        if (deadline < currentTime + tickMillis) return false;       // due now
        if (deadline < currentTime + interval) {
            long virtualId = Math.floorDiv(deadline, tickMillis);
            buckets[(int) Math.floorMod(virtualId, (long) wheelSize)].add(e);
            return true;
        }
        if (overflow == null) overflow = new TimingWheel(interval, wheelSize, currentTime);
        return overflow.add(e);
    }

    /**
     * Advance to {@code timeMillis}, tick by tick. Each tick expires the lowest wheel's current bucket, and
     * when the time crosses an overflow wheel's tick boundary, that wheel's bucket cascades down one level.
     * Entries that come due are handed to {@code due} in deadline-tick order, insertion order within a tick.
     * Only the lowest wheel is driven this way; it drives the wheels above it.
     */
    void advanceTo(long timeMillis, Consumer<TimerEntry> due) {
        while (currentTime + tickMillis <= timeMillis) {
            currentTime += tickMillis;
            if (overflow != null) overflow.cascadeTo(currentTime, this, due);
            buckets[(int) Math.floorMod(Math.floorDiv(currentTime, tickMillis), (long) wheelSize)].flush(e -> {
                if (!e.isCancelled()) due.accept(e);
            });
        }
    }

    /**
     * Called by the wheel below when the lowest wheel reaches a time that may be one of our tick boundaries.
     * Entries in the bucket for the new current tick are re-inserted from the lowest wheel ({@code root}); each
     * lands in a finer bucket or, if its deadline is inside the current lowest tick, fires now.
     */
    private void cascadeTo(long timeMillis, TimingWheel root, Consumer<TimerEntry> due) {
        if (timeMillis < currentTime + tickMillis) return;           // not our boundary yet
        currentTime = timeMillis - Math.floorMod(timeMillis, tickMillis);
        if (overflow != null) overflow.cascadeTo(currentTime, root, due);
        buckets[(int) Math.floorMod(Math.floorDiv(currentTime, tickMillis), (long) wheelSize)].flush(e -> {
            if (e.isCancelled()) return;
            root.cascades++;
            if (!root.add(e)) due.accept(e);                         // deadline inside the current lowest tick
        });
    }

    int pending() {
        int n = 0;
        for (Bucket b : buckets) n += b.size();
        return n + (overflow == null ? 0 : overflow.pending());
    }
}

/** The public face: schedule by delay, cancel by handle, and drive from an injected clock. */
final class HierarchicalTimer {
    private final Clock clock;
    private final TimingWheel wheel;
    private long nextId = 1;
    private long fired;

    HierarchicalTimer(Clock clock, long tickMillis, int wheelSize) {
        this.clock = clock;
        this.wheel = new TimingWheel(tickMillis, wheelSize, clock.nowMillis());
    }

    /**
     * Schedule a task for now + delay. The wheel fires only on tick boundaries, so the deadline is rounded UP
     * to the next multiple of the tick: a timer is never early, and at most one tick late.
     */
    TimerEntry schedule(long delayMillis, Runnable task) {
        if (delayMillis < 0) throw new IllegalArgumentException("delay must be >= 0");
        long deadline = Math.addExact(clock.nowMillis(), delayMillis);
        long tick = wheel.tickMillis;
        long rounded = Math.multiplyExact(Math.ceilDiv(deadline, tick), tick);
        TimerEntry e = new TimerEntry(nextId++, rounded, task);
        if (!wheel.add(e) && e.fire()) fired++;                       // already due: run inline
        return e;
    }

    /** Process everything due at or before the clock's current time. */
    void poll() {
        wheel.advanceTo(clock.nowMillis(), e -> { if (e.fire()) fired++; });
    }

    int pending() { return wheel.pending(); }
    long fired() { return fired; }
    int levels() { return wheel.levels(); }
    int cascades() { return wheel.cascades; }
    long tickMillis() { return wheel.tickMillis; }
}

/** The structure a wheel is usually compared with: a binary heap keyed by deadline, with lazy cancellation. */
final class HeapTimer {
    private final Clock clock;
    private final PriorityQueue<TimerEntry> heap = new PriorityQueue<>((a, b) -> {
        int c = Long.compare(a.deadlineMillis, b.deadlineMillis);
        return c != 0 ? c : Long.compare(a.id, b.id);
    });
    private long nextId = 1;
    private long fired;

    HeapTimer(Clock clock) { this.clock = clock; }

    TimerEntry schedule(long delayMillis, Runnable task) {
        TimerEntry e = new TimerEntry(nextId++, Math.addExact(clock.nowMillis(), delayMillis), task);
        heap.add(e);                                                  // O(log n)
        return e;
    }

    /** Cancelled entries stay in the heap until they surface: O(1) cancel, memory until the deadline. */
    void poll() {
        long now = clock.nowMillis();
        while (!heap.isEmpty() && heap.peek().deadlineMillis <= now) {
            TimerEntry e = heap.poll();
            if (e.fire()) fired++;
        }
    }

    int pending() { return heap.size(); }
    long fired() { return fired; }
}

public final class Main {
    public static void main(String[] args) {
        System.out.println("== 1. Authorization holds: schedule, fire, cancel on capture ==");
        ManualClock clock = new ManualClock(1_000_000);
        HierarchicalTimer timer = new HierarchicalTimer(clock, 1, 8);          // 1 ms tick, 8 buckets per level
        List<String> log = new ArrayList<>();
        timer.schedule(3, () -> log.add("auth-A expired"));
        TimerEntry authB = timer.schedule(5, () -> log.add("auth-B expired"));
        timer.schedule(5, () -> log.add("auth-C expired"));
        timer.schedule(12, () -> log.add("auth-D expired (beyond the 8 ms wheel: overflow level)"));
        System.out.println("  levels after scheduling = " + timer.levels() + ", pending = " + timer.pending());
        clock.advance(4);
        timer.poll();
        System.out.println("  t+4:  " + log);
        boolean cancelled = authB.cancel();
        System.out.println("  auth-B captured, cancel() = " + cancelled + " (O(1) unlink), pending = " + timer.pending());
        clock.advance(8);
        timer.poll();
        System.out.println("  t+12: " + log);
        System.out.println("  cascades from the overflow wheel = " + timer.cascades());

        System.out.println();
        System.out.println("== 2. Three levels: 1 ms x 8 covers 8 ms, 8 ms x 8 covers 64 ms, 64 ms x 8 covers 512 ms ==");
        ManualClock clock2 = new ManualClock(0);
        HierarchicalTimer t2 = new HierarchicalTimer(clock2, 1, 8);
        List<Long> fireTimes = new ArrayList<>();
        long[] delays = {1, 7, 8, 9, 63, 64, 65, 300, 511};
        for (long d : delays) {
            t2.schedule(d, () -> fireTimes.add(clock2.nowMillis()));
        }
        System.out.println("  delays " + java.util.Arrays.toString(delays) + " -> levels = " + t2.levels());
        long last = 0;
        for (long d : delays) {
            clock2.advance(d - last);
            last = d;
            t2.poll();
        }
        System.out.println("  fired at   " + fireTimes + " (each at exactly its deadline)");
        System.out.println("  cascades = " + t2.cascades() + " (one per entry per level it descended)");

        System.out.println();
        System.out.println("== 3. Tick granularity: a 10 ms tick cannot tell 23 ms from 29 ms ==");
        ManualClock clock3 = new ManualClock(0);
        HierarchicalTimer coarse = new HierarchicalTimer(clock3, 10, 16);
        List<String> order = new ArrayList<>();
        coarse.schedule(29, () -> order.add("29ms"));
        coarse.schedule(23, () -> order.add("23ms"));
        clock3.advance(25);
        coarse.poll();
        System.out.println("  at t=25 fired: " + order + " (nothing: both sit in the tick that ends at 30)");
        clock3.advance(5);
        coarse.poll();
        System.out.println("  at t=30 fired: " + order + " (insertion order, not deadline order, within a tick)");

        System.out.println();
        System.out.println("== 4. Wheel versus heap on the same seeded workload ==");
        Random rng = new Random(2024);
        ManualClock wc = new ManualClock(0), hc = new ManualClock(0);
        HierarchicalTimer wheelT = new HierarchicalTimer(wc, 1, 64);
        HeapTimer heapT = new HeapTimer(hc);
        List<Long> wheelOrder = new ArrayList<>(), heapOrder = new ArrayList<>();
        int n = 200_000;
        List<TimerEntry> wheelEntries = new ArrayList<>(n), heapEntries = new ArrayList<>(n);
        for (int i = 0; i < n; i++) {
            long delay = 1 + rng.nextInt(600_000);                                   // up to 10 minutes
            final long id = i;
            wheelEntries.add(wheelT.schedule(delay, () -> wheelOrder.add(id)));
            heapEntries.add(heapT.schedule(delay, () -> heapOrder.add(id)));
        }
        int cancelledCount = 0;
        for (int i = 0; i < n; i += 3) {                                             // capture one in three holds
            wheelEntries.get(i).cancel();
            heapEntries.get(i).cancel();
            cancelledCount++;
        }
        System.out.println("  scheduled " + n + ", cancelled " + cancelledCount + " -> wheel pending " + wheelT.pending()
                + " (freed on cancel), heap pending " + heapT.pending() + " (freed lazily when polled)");
        System.out.println("  wheel levels = " + wheelT.levels() + " for deadlines up to 600000 ms with 1 ms x 64 buckets");
        for (int step = 0; step < 600; step++) {
            wc.advance(1_000);
            hc.advance(1_000);
            wheelT.poll();
            heapT.poll();
        }
        wc.advance(1_000);
        hc.advance(1_000);
        wheelT.poll();
        heapT.poll();
        System.out.println("  fired: wheel " + wheelT.fired() + ", heap " + heapT.fired() + ", expected " + (n - cancelledCount));
        System.out.println("  same set of timers fired = " + sameSetFired(wheelOrder, heapOrder));
        System.out.println("  cascades = " + wheelT.cascades() + " (each entry moves down at most levels-1 times)");

        System.out.println();
        System.out.println("== 5. Cost model ==");
        System.out.println("  wheel: insert O(1), cancel O(1), tick O(1 + due); an idle wheel still pays one tick per tickMillis");
        System.out.println("  heap:  insert O(log n), cancel O(1) lazy or O(n) eager, poll O(log n) per due; idle heap costs nothing");
        System.out.println("  with n = " + n + ": log2(n) = " + (64 - Long.numberOfLeadingZeros(n)) + " comparisons per heap insert versus one bucket link");
    }

    /** Within a tick the two structures may order differently, so compare the fired sets, not the sequences. */
    static boolean sameSetFired(List<Long> a, List<Long> b) {
        if (a.size() != b.size()) return false;
        List<Long> sa = new ArrayList<>(a), sb = new ArrayList<>(b);
        sa.sort(null);
        sb.sort(null);
        return sa.equals(sb);
    }
}
