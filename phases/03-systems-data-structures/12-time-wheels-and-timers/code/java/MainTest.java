// Tests for phases/03-systems-data-structures/12-time-wheels-and-timers. Run: javac -d .out *.java && java -cp .out MainTest
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
        // Bucket: O(1) link and unlink, FIFO flush
        Bucket bucket = new Bucket();
        TimerEntry e1 = new TimerEntry(1, 10, () -> {}), e2 = new TimerEntry(2, 10, () -> {}), e3 = new TimerEntry(3, 10, () -> {});
        bucket.add(e1); bucket.add(e2); bucket.add(e3);
        check("bucket size after three adds", bucket.size() == 3);
        bucket.remove(e2);
        check("bucket remove middle entry", bucket.size() == 2 && e2.bucket == null && e2.prev == null);
        List<Long> flushed = new ArrayList<>();
        bucket.flush(e -> flushed.add(e.id));
        check("bucket flush is FIFO and empties the bucket", flushed.equals(List.of(1L, 3L)) && bucket.size() == 0);
        bucket.remove(e1);
        check("bucket remove of a non-member is a no-op", bucket.size() == 0);

        // Single wheel: exact firing on tick boundaries
        ManualClock clock = new ManualClock(0);
        HierarchicalTimer timer = new HierarchicalTimer(clock, 1, 8);
        List<String> log = new ArrayList<>();
        timer.schedule(3, () -> log.add("a"));
        timer.schedule(5, () -> log.add("b"));
        timer.schedule(5, () -> log.add("c"));
        check("nothing fires before the deadline", poll(timer, clock, 2) && log.isEmpty());
        check("fires exactly at the deadline", poll(timer, clock, 1) && log.equals(List.of("a")));
        check("same-tick timers fire in insertion order", poll(timer, clock, 2) && log.equals(List.of("a", "b", "c")));
        check("pending drops to zero", timer.pending() == 0 && timer.fired() == 3);

        // Zero delay runs inline; negative delay rejected
        List<String> inline = new ArrayList<>();
        timer.schedule(0, () -> inline.add("now"));
        check("zero delay on a tick boundary runs inline", inline.equals(List.of("now")));
        checkThrows("negative delay is rejected", IllegalArgumentException.class, () -> timer.schedule(-1, () -> {}));
        checkThrows("wheel size 1 is rejected", IllegalArgumentException.class, () -> new TimingWheel(1, 1, 0));
        checkThrows("clock does not go backwards", IllegalArgumentException.class, () -> clock.advance(-1));

        // Cancellation is O(1) and frees the slot immediately
        ManualClock c2 = new ManualClock(0);
        HierarchicalTimer t2 = new HierarchicalTimer(c2, 1, 8);
        List<String> l2 = new ArrayList<>();
        TimerEntry hold = t2.schedule(4, () -> l2.add("hold expired"));
        t2.schedule(4, () -> l2.add("other"));
        check("cancel returns true the first time", hold.cancel());
        check("cancel returns false the second time", !hold.cancel());
        check("cancel frees the bucket slot immediately", t2.pending() == 1 && hold.bucket == null);
        poll(t2, c2, 4);
        check("cancelled timer never fires, neighbour does", l2.equals(List.of("other")));
        TimerEntry pastHold = t2.schedule(2, () -> l2.add("late cancel"));
        poll(t2, c2, 2);
        check("cancel after firing is false and harmless", !pastHold.cancel() && l2.equals(List.of("other", "late cancel")));

        // Overflow wheels: a delay beyond the interval creates a level and cascades back down
        ManualClock c3 = new ManualClock(0);
        HierarchicalTimer t3 = new HierarchicalTimer(c3, 1, 8);
        check("one level for a delay inside the interval", levelsAfter(t3, 7) == 1);
        check("delay == interval opens an overflow wheel", levelsAfter(t3, 8) == 2);
        check("delay 63 stays at two levels", levelsAfter(t3, 63) == 2);
        check("delay 64 opens a third level", levelsAfter(t3, 64) == 3);
        check("delay 511 stays at three levels", levelsAfter(t3, 511) == 3);
        check("delay 512 opens a fourth level", levelsAfter(t3, 512) == 4);

        ManualClock c4 = new ManualClock(0);
        HierarchicalTimer t4 = new HierarchicalTimer(c4, 1, 8);
        List<Long> fireTimes = new ArrayList<>();
        long[] delays = {1, 7, 8, 9, 63, 64, 65, 300, 511, 512, 4095};
        for (long d : delays) t4.schedule(d, () -> fireTimes.add(c4.nowMillis()));
        for (int t = 0; t < 4096; t++) { c4.advance(1); t4.poll(); }
        List<Long> expected = new ArrayList<>();
        for (long d : delays) expected.add(d);
        check("every delay fires at exactly its deadline across four levels", fireTimes.equals(expected));
        // 1,7: level 0 (0). 8,9,63: level 1 (1 each). 64,65: level 2 but land in level 0 directly (1 each). 300,511: level 2 via level 1 (2 each).
        // 512: level 3, due at its boundary (1). 4095: level 3 -> 2 -> 1 -> 0 (3). Total 13; never more than levels - 1 per entry.
        check("cascades: one per level an entry descends, 13 for this workload", t4.cascades() == 13);

        // Cascade correctness when the clock is not advanced one tick at a time
        ManualClock c5 = new ManualClock(0);
        HierarchicalTimer t5 = new HierarchicalTimer(c5, 1, 8);
        List<Long> jumpFires = new ArrayList<>();
        t5.schedule(70, () -> jumpFires.add(c5.nowMillis()));
        t5.schedule(9, () -> jumpFires.add(c5.nowMillis()));
        c5.advance(100);
        t5.poll();
        check("a big clock jump still fires everything due, in deadline order", jumpFires.equals(List.of(100L, 100L)) && t5.fired() == 2);

        // Non-zero start time and non-aligned clock: rounding up, never early
        ManualClock c6 = new ManualClock(1_003);
        HierarchicalTimer coarse = new HierarchicalTimer(c6, 10, 16);
        List<String> order = new ArrayList<>();
        coarse.schedule(29, () -> order.add("29"));   // deadline 1032 -> tick 1040
        coarse.schedule(23, () -> order.add("23"));   // deadline 1026 -> tick 1030
        c6.advance(26);                                // now 1029
        coarse.poll();
        check("coarse tick: nothing fires before its rounded-up tick", order.isEmpty());
        c6.advance(1);                                 // 1030
        coarse.poll();
        check("coarse tick: 23 ms fires at the 1030 boundary", order.equals(List.of("23")));
        c6.advance(10);                                // 1040
        coarse.poll();
        check("coarse tick: 29 ms fires at the 1040 boundary", order.equals(List.of("23", "29")));
        check("rounded deadline is a tick multiple", coarse.schedule(5, () -> {}).deadlineMillis % 10 == 0);

        // Wheel and heap agree on a seeded workload with cancellations
        Random rng = new Random(99);
        ManualClock wc = new ManualClock(0), hc = new ManualClock(0);
        HierarchicalTimer wheel = new HierarchicalTimer(wc, 1, 16);
        HeapTimer heap = new HeapTimer(hc);
        List<Long> wheelFired = new ArrayList<>(), heapFired = new ArrayList<>();
        List<TimerEntry> we = new ArrayList<>(), he = new ArrayList<>();
        for (int i = 0; i < 5_000; i++) {
            long delay = rng.nextInt(20_000);
            final long id = i;
            we.add(wheel.schedule(delay, () -> wheelFired.add(id)));
            he.add(heap.schedule(delay, () -> heapFired.add(id)));
        }
        for (int i = 0; i < 5_000; i += 4) { we.get(i).cancel(); he.get(i).cancel(); }
        for (int step = 0; step < 25; step++) {
            wc.advance(1_000); hc.advance(1_000);
            wheel.poll(); heap.poll();
        }
        check("wheel and heap fire the same set", Main.sameSetFired(wheelFired, heapFired) && wheelFired.size() == 3_750);
        check("wheel frees cancelled entries eagerly, heap lazily", wheel.pending() == 0 && heap.pending() == 0 && wheel.fired() == heap.fired());

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }

    static boolean poll(HierarchicalTimer timer, ManualClock clock, long advance) {
        clock.advance(advance);
        timer.poll();
        return true;
    }

    static int levelsAfter(HierarchicalTimer timer, long delay) {
        timer.schedule(delay, () -> {});
        return timer.levels();
    }
}
