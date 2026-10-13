// Tests for phases/01-complexity-and-core-structures/09-heaps-and-priority-queues. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.Comparator;
import java.util.List;
import java.util.NoSuchElementException;

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

    static boolean sortedAscending(List<Integer> xs) {
        for (int i = 1; i < xs.size(); i++) if (xs.get(i - 1) > xs.get(i)) return false;
        return true;
    }

    public static void main(String[] args) {
        // basic heap behaviour
        MinHeap<Integer> h = new MinHeap<>(Comparator.naturalOrder());
        check("empty heap has size 0", h.size() == 0 && h.isEmpty());
        checkThrows("peek on empty throws", NoSuchElementException.class, h::peek);
        checkThrows("pop on empty throws", NoSuchElementException.class, h::pop);
        h.push(5); h.push(3); h.push(8); h.push(1);
        check("peek is the minimum", h.peek() == 1);
        check("push keeps heap property", h.isHeap());
        check("pop returns min then next min", h.pop() == 1 && h.pop() == 3 && h.pop() == 5 && h.pop() == 8);
        check("heap empty after popping everything", h.isEmpty());
        h.push(7); h.push(7); h.push(7);
        check("duplicates are allowed", h.size() == 3 && h.pop() == 7);

        // heapify builds a valid heap in one pass and pops sorted
        List<Integer> raw = List.of(9, 4, 7, 1, 8, 2, 6, 3, 5, 0, 11, 10);
        MinHeap<Integer> built = MinHeap.heapify(new ArrayList<>(raw), Comparator.naturalOrder());
        check("heapify preserves size", built.size() == raw.size());
        check("heapify satisfies heap property", built.isHeap());
        List<Integer> drained = new ArrayList<>();
        while (!built.isEmpty()) drained.add(built.pop());
        check("heapify then pop yields sorted output", sortedAscending(drained) && drained.size() == raw.size());
        check("heapify of empty list", MinHeap.heapify(new ArrayList<Integer>(), Comparator.naturalOrder()).isEmpty());
        check("heapify of one element", MinHeap.heapify(new ArrayList<>(List.of(42)), Comparator.naturalOrder()).peek() == 42);
        MinHeap<Integer> reversed = MinHeap.heapify(new ArrayList<>(raw), Comparator.reverseOrder());
        check("comparator controls the order: reverse gives a max-heap", reversed.peek() == 11 && reversed.isHeap());

        // heap property after seeded random operations, checked after every op
        SeededRandom rng = new SeededRandom(42);
        MinHeap<Integer> stress = new MinHeap<>(Comparator.naturalOrder());
        List<Integer> shadow = new ArrayList<>();
        boolean alwaysHeap = true, popsMatchShadow = true;
        for (int i = 0; i < 2_000; i++) {
            if (stress.isEmpty() || rng.nextInt(3) != 0) {
                int v = rng.nextInt(500);
                stress.push(v); shadow.add(v);
            } else {
                int popped = stress.pop();
                int min = shadow.stream().min(Integer::compare).orElseThrow();
                if (popped != min) popsMatchShadow = false;
                shadow.remove(Integer.valueOf(min));
            }
            if (!stress.isHeap()) alwaysHeap = false;
        }
        check("seeded random ops: heap property held after every operation", alwaysHeap);
        check("seeded random ops: every pop returned the true minimum", popsMatchShadow);
        check("seeded random ops: size matches shadow list", stress.size() == shadow.size());

        // indexed heap: decrease-key and stable ties
        IndexedMinHeap<String> ih = new IndexedMinHeap<>();
        ih.push("A", 50); ih.push("B", 50); ih.push("C", 50); ih.push("D", 10);
        check("indexed: consistent after pushes", ih.isConsistent());
        check("indexed: contains and priorityOf", ih.contains("B") && ih.priorityOf("B") == 50 && !ih.contains("Z"));
        checkThrows("indexed: duplicate key rejected", IllegalArgumentException.class, () -> ih.push("A", 1));
        check("indexed: lowest priority pops first", ih.pop().key().equals("D"));
        check("indexed: equal priorities pop in insertion order", ih.pop().key().equals("A") && ih.pop().key().equals("B") && ih.pop().key().equals("C"));
        check("indexed: empty after draining", ih.isEmpty() && ih.isConsistent());

        IndexedMinHeap<String> dk = new IndexedMinHeap<>();
        for (int i = 0; i < 20; i++) dk.push("k" + i, 1_000 + i * 10);
        dk.decreaseKey("k17", 5);
        check("decreaseKey: moves the key to the top", dk.peek().key().equals("k17") && dk.priorityOf("k17") == 5);
        check("decreaseKey: index map still consistent", dk.isConsistent());
        dk.decreaseKey("k3", 1_000);     // becomes a tie with k0, which was inserted earlier
        check("decreaseKey: a new tie keeps insertion order (k0 before k3)", dk.pop().key().equals("k17") && dk.pop().key().equals("k0") && dk.pop().key().equals("k3"));
        checkThrows("decreaseKey: raising a priority is rejected", IllegalArgumentException.class, () -> dk.decreaseKey("k5", 999_999));
        checkThrows("decreaseKey: unknown key is rejected", NoSuchElementException.class, () -> dk.decreaseKey("nope", 1));
        check("decreaseKey: consistent after mixed operations", dk.isConsistent());

        // retry scheduler on a fake clock
        FakeClock clock = new FakeClock(1_000_000);
        RetryScheduler s = new RetryScheduler(clock);
        check("backoff doubles from the base", RetryScheduler.backoffMillis(0) == 1_000 && RetryScheduler.backoffMillis(3) == 8_000);
        check("backoff is capped", RetryScheduler.backoffMillis(6) == 60_000 && RetryScheduler.backoffMillis(40) == 60_000);
        s.scheduleRetry("A", 2);        // +4000
        s.scheduleRetry("B", 0);        // +1000
        s.scheduleRetry("C", 0);        // +1000, tie with B
        s.scheduleRetry("D", 6);        // +60000
        check("scheduler: nothing due yet", s.pollDue().isEmpty() && s.pending() == 4);
        s.expedite("D", clock.nowMillis() + 500);
        check("scheduler: expedite moves D to the front", s.nextDueAtMillis() == clock.nowMillis() + 500);
        checkThrows("scheduler: expedite cannot push a retry later", IllegalArgumentException.class, () -> s.expedite("A", clock.nowMillis() + 999_999));
        clock.advance(1_000);
        List<RetryJob> due = s.pollDue();
        check("scheduler: due jobs come out earliest first, ties in schedule order",
                due.size() == 3 && due.get(0).paymentId().equals("D") && due.get(1).paymentId().equals("B") && due.get(2).paymentId().equals("C"));
        check("scheduler: polled jobs are removed", s.pending() == 1 && !s.isScheduled("B") && s.isScheduled("A"));
        clock.advance(2_999);
        check("scheduler: one millisecond early is not due", s.pollDue().isEmpty());
        clock.advance(1);
        check("scheduler: due exactly at the boundary", s.pollDue().size() == 1 && s.pending() == 0);
        checkThrows("clock cannot go backwards", IllegalArgumentException.class, () -> clock.advance(-1));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
