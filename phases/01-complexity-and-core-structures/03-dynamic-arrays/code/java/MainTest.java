// Tests for phases/01-complexity-and-core-structures/03-dynamic-arrays. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
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

    /** The structural invariant: size within capacity, live prefix intact, dead suffix null. */
    static boolean invariantHolds(DynamicArray<Integer> a, int expectedSize) {
        if (a.size() != expectedSize || a.size() > a.capacity()) return false;
        for (int i = 0; i < a.size(); i++) if (a.get(i) == null) return false;
        return true;
    }

    public static void main(String[] args) {
        // growth policies
        check("1.5x: 8 -> 12", GrowthPolicy.GROW_1_5X.next(8) == 12);
        check("1.5x: 1 -> 2 (never stalls)", GrowthPolicy.GROW_1_5X.next(1) == 2);
        check("1.5x: 2 -> 3", GrowthPolicy.GROW_1_5X.next(2) == 3);
        check("2x: 8 -> 16", GrowthPolicy.GROW_2X.next(8) == 16);
        check("2x: 1 -> 2", GrowthPolicy.GROW_2X.next(1) == 2);

        // capacity vs size and the add path
        DynamicArray<Integer> a = new DynamicArray<>(4, GrowthPolicy.GROW_2X);
        for (int i = 0; i < 4; i++) a.add(i);
        check("4 adds fill capacity 4 without realloc", a.capacity() == 4 && a.stats().reallocations == 0);
        a.add(4);
        check("5th add doubles capacity", a.capacity() == 8 && a.size() == 5);
        check("5th add copied the 4 existing elements", a.stats().elementsCopied == 4 && a.stats().reallocations == 1);
        check("elements preserved across grow", a.get(0) == 0 && a.get(3) == 3 && a.get(4) == 4);
        check("invariant after grow", invariantHolds(a, 5));

        // bounds are checked against size, not capacity
        checkThrows("get(size) rejected even though slot exists", IndexOutOfBoundsException.class, () -> a.get(5));
        checkThrows("get(-1) rejected", IndexOutOfBoundsException.class, () -> a.get(-1));
        checkThrows("set past size rejected", IndexOutOfBoundsException.class, () -> a.set(7, 1));
        checkThrows("insert past size rejected", IndexOutOfBoundsException.class, () -> a.insert(6, 1));
        check("insert at size is allowed (append)", run(() -> { a.insert(a.size(), 99); }) && a.get(5) == 99);

        // insert and remove shift correctly
        DynamicArray<Integer> b = new DynamicArray<>(2, GrowthPolicy.GROW_1_5X, false);
        b.add(10); b.add(30);
        b.insert(1, 20);
        check("insert in the middle shifts tail", b.get(0) == 10 && b.get(1) == 20 && b.get(2) == 30 && b.size() == 3);
        check("insert into full array grew by 1.5x rule (2 -> 3)", b.capacity() == 3);
        check("remove middle returns and shifts", b.remove(1) == 20 && b.get(1) == 30 && b.size() == 2);
        check("set returns old", b.set(0, 11) == 10 && b.get(0) == 11);
        check("removeLast", b.removeLast() == 30 && b.size() == 1);
        b.removeLast();
        checkThrows("removeLast on empty", NoSuchElementException.class, b::removeLast);

        // amortized copies: total copied is bounded by a constant times n
        int n = 100_000;
        ArrayStats two = Main.measureAppends(GrowthPolicy.GROW_2X, n, 8);
        ArrayStats onePointFive = Main.measureAppends(GrowthPolicy.GROW_1_5X, n, 8);
        check("2x copies fewer than 2n", two.elementsCopied < 2L * n);
        check("1.5x copies fewer than 3n", onePointFive.elementsCopied < 3L * n);
        check("1.5x copies more than 2x", onePointFive.elementsCopied > two.elementsCopied);
        check("1.5x reallocates more often", onePointFive.reallocations > two.reallocations);
        check("1.5x slack is below 50%", onePointFive.peakCapacity < 1.5 * n);
        check("2x slack is below 100%", two.peakCapacity < 2L * n);
        check("2x peak capacity is a power of two times 8", Integer.bitCount(two.peakCapacity / 8) == 1);
        check("sequence 1.5x from 8: 8,12,18,27,40", java.util.Arrays.equals(
                java.util.Arrays.copyOf(DynamicArray.capacitySequence(GrowthPolicy.GROW_1_5X, 8, 40), 5), new int[] {8, 12, 18, 27, 40}));

        // shrink policy with hysteresis
        DynamicArray<Integer> c = new DynamicArray<>(4, GrowthPolicy.GROW_2X);
        for (int i = 0; i < 64; i++) c.add(i);
        check("capacity 64 after 64 adds", c.capacity() == 64);
        while (c.size() > 16) c.removeLast();
        check("no shrink at exactly one quarter full", c.capacity() == 64 && c.stats().shrinks == 0);
        c.removeLast();
        check("shrink to half when below one quarter", c.capacity() == 32 && c.size() == 15 && c.stats().shrinks == 1);
        check("invariant after shrink", invariantHolds(c, 15));
        long reallocs = c.stats().reallocations;
        for (int i = 0; i < 500; i++) { c.add(i); c.removeLast(); }
        check("add/remove at boundary does not thrash", c.stats().reallocations == reallocs);
        while (c.size() > 0) c.removeLast();
        check("never shrinks below MIN_CAPACITY", c.capacity() == DynamicArray.MIN_CAPACITY);
        DynamicArray<Integer> d = new DynamicArray<>(4, GrowthPolicy.GROW_2X, false);
        for (int i = 0; i < 64; i++) d.add(i);
        d.clear();
        check("shrink disabled keeps capacity", d.capacity() == 64 && d.isEmpty());

        // iteration and construction errors
        DynamicArray<String> e = new DynamicArray<>(1, GrowthPolicy.GROW_1_5X);
        e.add("a"); e.add("b"); e.add("c");
        StringBuilder sb = new StringBuilder();
        for (String s : e) sb.append(s);
        check("iterator visits live prefix only", sb.toString().equals("abc"));
        checkThrows("initial capacity must be positive", IllegalArgumentException.class, () -> new DynamicArray<Integer>(0, GrowthPolicy.GROW_2X));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }

    private static boolean run(Runnable r) {
        try { r.run(); return true; } catch (RuntimeException ex) { return false; }
    }
}
