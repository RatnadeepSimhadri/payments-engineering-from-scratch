// Tests for phases/01-complexity-and-core-structures/04-linked-lists. Run: javac -d .out *.java && java -cp .out MainTest
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

    public static void main(String[] args) {
        // singly linked list
        SinglyLinkedList<Integer> s = new SinglyLinkedList<>();
        check("singly starts empty", s.isEmpty() && s.size() == 0);
        s.addLast(2); s.addLast(3); s.addFirst(1);
        check("singly addFirst/addLast order", s.toString().equals("[1, 2, 3]") && s.size() == 3);
        check("singly get walks to index", s.get(0) == 1 && s.get(2) == 3);
        checkThrows("singly get out of range", IndexOutOfBoundsException.class, () -> s.get(3));
        check("singly removeFirst", s.removeFirst() == 1 && s.size() == 2);
        check("singly remove by value in middle", s.removeFirstOccurrence(2) && s.toString().equals("[3]"));
        check("singly remove missing value is false", !s.removeFirstOccurrence(99));
        check("singly remove tail updates tail", s.removeFirstOccurrence(3) && s.isEmpty());
        s.addLast(10);
        check("singly addLast after emptying works (tail was reset)", s.get(0) == 10 && s.size() == 1);
        checkThrows("singly removeFirst on empty", NoSuchElementException.class, () -> new SinglyLinkedList<Integer>().removeFirst());
        SinglyLinkedList<Integer> r = new SinglyLinkedList<>();
        for (int i = 1; i <= 5; i++) r.addLast(i);
        r.reverse();
        check("singly reverse", r.toString().equals("[5, 4, 3, 2, 1]"));
        r.addLast(0);
        check("singly reverse keeps tail correct", r.toString().equals("[5, 4, 3, 2, 1, 0]"));

        // doubly linked list with sentinel
        DoublyLinkedList<String> d = new DoublyLinkedList<>();
        check("doubly empty invariant", d.invariantHolds() && d.first() == null && d.last() == null);
        DoublyLinkedList.Node<String> a = d.addLast("a");
        DoublyLinkedList.Node<String> c = d.addLast("c");
        DoublyLinkedList.Node<String> b = d.insertBefore(c, "b");
        check("doubly insertBefore", d.toString().equals("[a, b, c]") && d.size() == 3);
        check("doubly invariant after inserts", d.invariantHolds());
        check("doubly first/last", d.first() == a && d.last() == c);
        check("doubly unlink middle is O(1) and returns value", d.unlink(b).equals("b") && d.toString().equals("[a, c]"));
        check("doubly unlinked node is disowned", b.owner == null && b.prev == null && b.next == null);
        checkThrows("doubly unlink twice rejected", IllegalArgumentException.class, () -> d.unlink(b));
        checkThrows("doubly unlink foreign node rejected", IllegalArgumentException.class,
                () -> new DoublyLinkedList<String>().unlink(a));
        d.addFirst("z");
        check("doubly addFirst", d.toString().equals("[z, a, c]"));
        d.moveToFront(c);
        check("doubly moveToFront", d.toString().equals("[c, z, a]") && d.invariantHolds());
        d.moveToFront(c);
        check("doubly moveToFront on head is a no-op", d.toString().equals("[c, z, a]") && d.size() == 3);
        check("doubly removeFirst/removeLast", d.removeFirst().equals("c") && d.removeLast().equals("a") && d.toString().equals("[z]"));
        d.removeLast();
        check("doubly empty again with invariant", d.isEmpty() && d.invariantHolds());
        checkThrows("doubly removeLast on empty", NoSuchElementException.class, d::removeLast);

        // splice
        DoublyLinkedList<Integer> x = new DoublyLinkedList<>();
        DoublyLinkedList<Integer> y = new DoublyLinkedList<>();
        for (int i = 1; i <= 3; i++) x.addLast(i);
        for (int i = 4; i <= 6; i++) y.addLast(i);
        DoublyLinkedList.Node<Integer> five = y.first().next;
        x.spliceAll(y);
        check("splice appends all in order", x.toString().equals("[1, 2, 3, 4, 5, 6]") && x.size() == 6);
        check("splice empties the source", y.isEmpty() && y.invariantHolds());
        check("splice transfers ownership so nodes can be unlinked from the new list", five.owner == x && x.unlink(five) == 5);
        check("splice invariant", x.invariantHolds());
        x.spliceAll(y);
        check("splice of empty list is a no-op", x.size() == 5);
        checkThrows("splice into itself rejected", IllegalArgumentException.class, () -> x.spliceAll(x));

        // intrusive list
        IntrusiveList<PendingAuthorization> p = new IntrusiveList<>();
        PendingAuthorization p1 = new PendingAuthorization("p1", 100, 1);
        PendingAuthorization p2 = new PendingAuthorization("p2", 200, 2);
        PendingAuthorization p3 = new PendingAuthorization("p3", 300, 3);
        p.addLast(p1); p.addLast(p2); p.addLast(p3);
        check("intrusive size and first", p.size() == 3 && p.first() == p1);
        check("intrusive contains is O(1) via owner", p.contains(p2) && p1.isLinked());
        p.remove(p2);
        check("intrusive remove middle", p.size() == 2 && !p.contains(p2) && !p2.isLinked());
        StringBuilder sb = new StringBuilder();
        for (PendingAuthorization e : p) sb.append(e.id);
        check("intrusive iteration order", sb.toString().equals("p1p3"));
        checkThrows("intrusive remove twice rejected", IllegalArgumentException.class, () -> p.remove(p2));
        checkThrows("intrusive element cannot be in two lists", IllegalStateException.class,
                () -> new IntrusiveList<PendingAuthorization>().addLast(p1));
        IntrusiveList<PendingAuthorization> q = new IntrusiveList<>();
        q.addLast(p2);
        check("intrusive element can move to another list after removal", q.contains(p2) && !p.contains(p2));
        checkThrows("intrusive remove from wrong list rejected", IllegalArgumentException.class, () -> p.remove(p2));

        // benchmark helpers compute the same sum regardless of link order
        int n = 10_000;
        long[] arr = new long[n];
        Cell[] cells = new Cell[n];
        for (int i = 0; i < n; i++) { arr[i] = i; cells[i] = new Cell(i); }
        long expected = (long) n * (n - 1) / 2;
        check("sumArray", Main.sumArray(arr) == expected);
        IntrusiveList<Cell> seq = Main.linkInOrder(cells, Main.identity(n));
        check("sumList sequential", Main.sumList(seq) == expected);
        int[] perm = Main.shuffled(n, 7L);
        boolean isPermutation = java.util.Arrays.equals(java.util.Arrays.stream(perm).sorted().toArray(), Main.identity(n));
        check("shuffled is a permutation", isPermutation && !java.util.Arrays.equals(perm, Main.identity(n)));
        check("shuffled is deterministic for a seed", java.util.Arrays.equals(perm, Main.shuffled(n, 7L)));
        seq.clear();
        check("clear detaches every element", seq.isEmpty() && !cells[0].isLinked() && !cells[n - 1].isLinked());
        check("sumList shuffled order gives the same sum", Main.sumList(Main.linkInOrder(cells, perm)) == expected);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
