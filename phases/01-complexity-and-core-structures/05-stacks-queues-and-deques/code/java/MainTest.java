// Tests for phases/01-complexity-and-core-structures/05-stacks-queues-and-deques. Run: javac -d .out *.java && java -cp .out MainTest
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

    static <T> String drain(RingBuffer<T> r) {
        StringBuilder sb = new StringBuilder();
        T v;
        while ((v = r.pollFirst()) != null) sb.append(v).append(' ');
        return sb.toString().trim();
    }

    public static void main(String[] args) {
        // stack
        ArrayStack<Integer> st = new ArrayStack<>(2);
        check("stack starts empty", st.isEmpty() && st.size() == 0);
        st.push(1); st.push(2); st.push(3);
        check("stack grows past initial capacity", st.capacity() == 4 && st.size() == 3);
        check("stack peek is top", st.peek() == 3);
        check("stack pops LIFO", st.pop() == 3 && st.pop() == 2 && st.pop() == 1);
        checkThrows("stack pop on empty", NoSuchElementException.class, st::pop);
        checkThrows("stack peek on empty", NoSuchElementException.class, st::peek);
        st.push(7); st.push(8);
        StringBuilder sb = new StringBuilder();
        for (int v : st) sb.append(v);
        check("stack iterates top-down", sb.toString().equals("87"));
        check("brackets balanced", Main.bracketsBalanced("{\"a\":[1,2,{\"b\":3}]}") && Main.bracketsBalanced(""));
        check("brackets unbalanced", !Main.bracketsBalanced("{\"a\":[1,2}") && !Main.bracketsBalanced(")(") && !Main.bracketsBalanced("(("));

        // ring buffer basics and the full/empty distinction
        RingBuffer<String> r = new RingBuffer<>(4, OverflowPolicy.REJECT);
        check("ring starts empty, not full", r.isEmpty() && !r.isFull() && r.size() == 0);
        check("ring poll on empty returns null", r.pollFirst() == null && r.pollLast() == null && r.peekFirst() == null && r.peekLast() == null);
        check("ring offers 4", r.offerLast("a") && r.offerLast("b") && r.offerLast("c") && r.offerLast("d"));
        check("ring full at capacity: size == capacity, no wasted slot", r.isFull() && r.size() == 4 && !r.isEmpty());
        check("ring REJECT returns false when full", !r.offerLast("e") && r.size() == 4);
        check("ring REJECT leaves content intact", r.peekFirst().equals("a") && r.peekLast().equals("d"));
        check("ring FIFO", r.pollFirst().equals("a") && r.pollFirst().equals("b"));
        check("ring sequences advance", r.headSequence() == 2 && r.tailSequence() == 4 && r.size() == 2);

        // wraparound
        check("ring wraps: offers land in slots 0 and 1", r.offerLast("e") && r.offerLast("f") && r.indexOf(4) == 0 && r.indexOf(5) == 1);
        check("ring logical order survives the wrap", r.toString().startsWith("[c, d, e, f]"));
        check("ring get(i) through the wrap", r.get(0).equals("c") && r.get(2).equals("e") && r.get(3).equals("f"));
        checkThrows("ring get past size", IndexOutOfBoundsException.class, () -> r.get(4));
        sb.setLength(0);
        for (String s : r) sb.append(s);
        check("ring iterator follows sequences across the wrap", sb.toString().equals("cdef"));
        check("ring pollLast from the wrapped end", r.pollLast().equals("f") && r.peekLast().equals("e") && r.tailSequence() == 5);
        check("ring drain order", drain(r).equals("c d e") && r.isEmpty());
        check("ring empty after drain keeps sequences (head == tail == 5)", r.headSequence() == 5 && r.tailSequence() == 5);

        // long-running wrap: 2,000 seeded random deque operations against java.util.ArrayDeque as the model
        RingBuffer<Integer> cyc = new RingBuffer<>(3, OverflowPolicy.REJECT);
        java.util.ArrayDeque<Integer> model = new java.util.ArrayDeque<>();
        java.util.Random rnd = new java.util.Random(11);
        boolean ok = true;
        for (int i = 0; i < 2000 && ok; i++) {
            switch (rnd.nextInt(4)) {
                case 0 -> { boolean accepted = cyc.offerLast(i); if (model.size() < 3) { model.addLast(i); ok &= accepted; } else ok &= !accepted; }
                case 1 -> { boolean accepted = cyc.offerFirst(i); if (model.size() < 3) { model.addFirst(i); ok &= accepted; } else ok &= !accepted; }
                case 2 -> ok &= java.util.Objects.equals(cyc.pollFirst(), model.pollFirst());
                default -> ok &= java.util.Objects.equals(cyc.pollLast(), model.pollLast());
            }
            ok &= cyc.size() == model.size() && java.util.Objects.equals(cyc.peekFirst(), model.peekFirst());
        }
        check("ring matches ArrayDeque over 2000 random operations with wraparound", ok);

        // deque operations
        RingBuffer<Integer> d = new RingBuffer<>(4, OverflowPolicy.REJECT);
        d.offerLast(2); d.offerLast(3);
        check("deque offerFirst when head is 0 rebases sequences and keeps order", d.offerFirst(1) && d.offerFirst(0) && d.get(0) == 0 && d.get(3) == 3 && d.size() == 4);
        check("deque full after 4", d.isFull() && !d.offerFirst(9));
        check("deque pollLast/pollFirst", d.pollLast() == 3 && d.pollFirst() == 0 && d.size() == 2 && d.peekFirst() == 1 && d.peekLast() == 2);

        // DROP_OLDEST semantics
        RingBuffer<Integer> drop = new RingBuffer<>(4, OverflowPolicy.DROP_OLDEST);
        for (int i = 0; i < 10; i++) check("drop offer always succeeds " + i, drop.offerLast(i));
        check("drop keeps the newest capacity elements", drop.toString().startsWith("[6, 7, 8, 9]") && drop.size() == 4);
        check("drop counts evictions and advances head", drop.dropped() == 6 && drop.headSequence() == 6 && drop.tailSequence() == 10);
        check("drop offerFirst when full drops the newest", drop.offerFirst(5) && drop.toString().startsWith("[5, 6, 7, 8]") && drop.dropped() == 7);

        // construction and clear
        checkThrows("ring capacity must be positive", IllegalArgumentException.class, () -> new RingBuffer<Integer>(0, OverflowPolicy.REJECT));
        checkThrows("stack capacity must be positive", IllegalArgumentException.class, () -> new ArrayStack<Integer>(0));
        drop.clear();
        check("clear empties and resets", drop.isEmpty() && drop.headSequence() == 0 && drop.pollFirst() == null);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
