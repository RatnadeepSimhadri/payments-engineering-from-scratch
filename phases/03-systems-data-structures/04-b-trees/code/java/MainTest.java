// Tests for phases/03-systems-data-structures/04-b-trees. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.Random;
import java.util.TreeMap;

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

    static boolean isSorted(List<Integer> xs) {
        for (int i = 1; i < xs.size(); i++) if (xs.get(i - 1) >= xs.get(i)) return false;
        return true;
    }

    public static void main(String[] args) {
        // ----- construction and occupancy bounds -----
        checkThrows("order below 3 rejected", IllegalArgumentException.class, () -> new BTree<Integer, String>(2));
        BTree<Integer, String> o4 = new BTree<>(4);
        check("order 4: maxKeys 3, minKeys 1", o4.maxKeys == 3 && o4.minKeys == 1);
        BTree<Integer, String> o5 = new BTree<>(5);
        check("order 5: maxKeys 4, minKeys 2 (ceil(5/2)-1)", o5.maxKeys == 4 && o5.minKeys == 2);
        check("empty tree: height 1, size 0, get is null", o4.height() == 1 && o4.size() == 0 && o4.get(1) == null);

        // ----- the first split, by hand -----
        o4.put(10, "a"); o4.put(20, "b"); o4.put(30, "c");
        check("three keys fit in one order-4 node", o4.height() == 1 && o4.nodeCount() == 1 && o4.stats.splits == 0);
        o4.put(40, "d");                                             // overflow: split at median 20
        check("fourth key splits the root: height 2, 3 nodes, 1 split", o4.height() == 2 && o4.nodeCount() == 3 && o4.stats.splits == 1);
        check("split preserves order and content", o4.keys().equals(List.of(10, 20, 30, 40)) && o4.checkInvariants());
        check("get after split finds every key", "a".equals(o4.get(10)) && "b".equals(o4.get(20)) && "d".equals(o4.get(40)) && o4.get(25) == null);
        o4.put(20, "B");
        check("put existing key replaces value without growing", "B".equals(o4.get(20)) && o4.size() == 4);

        // ----- invariants, height bound, and page-read model on sequential input -----
        int n = 50_000;
        BTree<Integer, Integer> seq = new BTree<>(32);
        for (int i = 0; i < n; i++) seq.put(i, i);
        check("sequential inserts keep the invariants", seq.checkInvariants());
        check("size counts every key", seq.size() == n);
        check("height within Bayer-McCreight bound", seq.height() <= BTree.heightBound(32, n));
        check("height is logarithmic in fan-out (<= 5 levels for 50k keys at order 32)", seq.height() <= 5);
        check("keys() is sorted and complete", isSorted(seq.keys()) && seq.keys().size() == n);
        seq.stats.reset();
        seq.get(31_337);
        check("a point lookup reads exactly height pages", seq.stats.reads == seq.height());
        seq.stats.reset();
        List<Integer> hits = seq.range(1_000, 1_099);
        check("range returns the inclusive window in order", hits.size() == 100 && hits.get(0) == 1_000 && hits.get(99) == 1_099);
        check("range scan reads few pages beyond the leaves it touches", seq.stats.reads <= seq.height() + 100 / seq.minKeys + 2);
        check("range with no hits is empty", seq.range(n + 1, n + 100).isEmpty());
        check("range across whole tree equals keys()", seq.range(Integer.MIN_VALUE, Integer.MAX_VALUE).equals(seq.keys()));

        // ----- random input: same invariants, lower occupancy -----
        List<Integer> shuffled = new ArrayList<>();
        for (int i = 0; i < n; i++) shuffled.add(i);
        Collections.shuffle(shuffled, new Random(3));
        BTree<Integer, Integer> rnd = new BTree<>(32);
        for (int k : shuffled) rnd.put(k, k);
        check("random inserts keep the invariants", rnd.checkInvariants());
        check("random order yields the same key set", rnd.keys().equals(seq.keys()));
        double seqFill = (double) seq.size() / (seq.nodeCount() * seq.maxKeys), rndFill = (double) rnd.size() / (rnd.nodeCount() * rnd.maxKeys);
        check("naive median split leaves sequential pages ~half full", seqFill > 0.45 && seqFill < 0.55);
        check("random input fills pages to ~ln 2 (69%)", rndFill > 0.60 && rndFill < 0.78);
        TreeMap<Integer, Integer> reference = new TreeMap<>();
        for (int k : shuffled) reference.put(k, k);
        check("agrees with TreeMap on a range", rnd.range(123, 4567).equals(new ArrayList<>(reference.subMap(123, true, 4567, true).keySet())));

        // ----- deletes: borrow, merge, and shrink -----
        for (int i = 0; i < n; i += 2) rnd.remove(i);
        check("remove halves the size", rnd.size() == n / 2);
        check("invariants hold after mass deletes (borrow and merge paths exercised)", rnd.checkInvariants() && rnd.stats.merges > 0);
        check("removed keys gone, survivors present", rnd.get(0) == null && rnd.get(1_000) == null && rnd.get(1) == 1 && rnd.get(999) == 999);
        rnd.remove(-5);
        check("removing a missing key is a no-op", rnd.size() == n / 2 && rnd.checkInvariants());

        BTree<Integer, String> shrink = new BTree<>(4);
        for (int i = 0; i < 1_000; i++) shrink.put(i, "x");
        int before = shrink.height();
        for (int i = 0; i < 1_000; i++) shrink.remove(i);
        check("deleting everything shrinks height back to 1", before > 1 && shrink.height() == 1 && shrink.size() == 0 && shrink.checkInvariants());

        // the hand-traced deletes from the lesson: borrow, then merge, then separator replacement
        BTree<Integer, String> traced = new BTree<>(4);
        for (int i = 1; i <= 10; i++) traced.put(i, "v");
        traced.stats.reset();
        traced.remove(10);
        check("remove 10 borrows from [7 8]: still 5 nodes, no merge", traced.nodeCount() == 5 && traced.stats.merges == 0 && traced.checkInvariants());
        traced.remove(9);
        check("remove 9 merges [7] + 8 + []: 4 nodes, one merge", traced.nodeCount() == 4 && traced.stats.merges == 1 && traced.checkInvariants());
        traced.remove(6);
        check("remove separator 6 replaces it with predecessor 5", traced.nodeCount() == 4 && traced.keys().equals(List.of(1, 2, 3, 4, 5, 7, 8)) && traced.checkInvariants());

        // deleting an internal key: replaced by predecessor from a leaf
        BTree<Integer, String> internal = new BTree<>(4);
        for (int i = 1; i <= 10; i++) internal.put(i, "v");
        List<Integer> all = new ArrayList<>(internal.keys());
        boolean internalOk = true;
        for (int k : new int[]{4, 2, 8, 6}) {                           // separators in the order-4 tree built from 1..10
            internal.remove(k);
            all.remove(Integer.valueOf(k));
            internalOk &= internal.keys().equals(all) && internal.checkInvariants();
        }
        check("deleting separator keys keeps order and invariants", internalOk);

        // ----- randomized insert/delete churn against TreeMap -----
        Random rng = new Random(99);
        BTree<Integer, Integer> churn = new BTree<>(5);
        TreeMap<Integer, Integer> model = new TreeMap<>();
        boolean agree = true;
        for (int step = 0; step < 20_000 && agree; step++) {
            int key = rng.nextInt(2_000);
            if (rng.nextInt(3) == 0) { churn.remove(key); model.remove(key); }
            else { churn.put(key, step); model.put(key, step); }
            if (step % 1_000 == 999) agree = churn.keys().equals(new ArrayList<>(model.keySet())) && churn.checkInvariants() && churn.size() == model.size();
        }
        check("20k mixed operations agree with TreeMap and keep invariants", agree);

        // ----- lowerBound: the within-page binary search -----
        BTree.Node<Integer, String> node = new BTree.Node<>();
        node.keys.add(10); node.keys.add(20); node.keys.add(30);
        check("lowerBound below all keys is 0", BTree.lowerBound(node, 5) == 0);
        check("lowerBound on an exact key is its index", BTree.lowerBound(node, 20) == 1);
        check("lowerBound between keys picks the child slot", BTree.lowerBound(node, 25) == 2);
        check("lowerBound above all keys is keyCount", BTree.lowerBound(node, 31) == 3);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
