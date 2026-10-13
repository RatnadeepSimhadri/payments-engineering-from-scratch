// Tests for phases/03-systems-data-structures/03-balanced-trees. Run: javac -d .out *.java && java -cp .out MainTest
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

    public static void main(String[] args) {
        // ----- the failure the lesson warns about -----
        NaiveBst<Integer, String> naive = new NaiveBst<>();
        for (int i = 0; i < 1_000; i++) naive.put(i, "x");
        check("naive BST degenerates to height n on sorted input", naive.height() == 1_000);

        // ----- AVL: rotations by hand -----
        AvlTree<Integer, String> ll = new AvlTree<>();
        ll.put(30, "c"); ll.put(20, "b"); ll.put(10, "a");          // LL case: one right rotation
        check("AVL LL case: one rotation", ll.rotations() == 1);
        check("AVL LL case: height 2 with 20 at the root", ll.height() == 2 && ll.keys().equals(List.of(10, 20, 30)));
        AvlTree<Integer, String> lr = new AvlTree<>();
        lr.put(30, "c"); lr.put(10, "a"); lr.put(20, "b");          // LR case: two rotations
        check("AVL LR case: two rotations", lr.rotations() == 2);
        check("AVL LR case: balanced height 2", lr.height() == 2 && lr.checkInvariants());
        AvlTree<Integer, String> rr = new AvlTree<>();
        rr.put(10, "a"); rr.put(20, "b"); rr.put(30, "c");
        check("AVL RR case: one rotation", rr.rotations() == 1 && rr.height() == 2);
        AvlTree<Integer, String> rl = new AvlTree<>();
        rl.put(10, "a"); rl.put(30, "c"); rl.put(20, "b");
        check("AVL RL case: two rotations", rl.rotations() == 2 && rl.height() == 2);

        // ----- AVL: invariant and height bound on sorted and random input -----
        int n = 20_000;
        AvlTree<Integer, Integer> avlSorted = new AvlTree<>();
        for (int i = 0; i < n; i++) avlSorted.put(i, i);
        check("AVL invariants hold after sorted inserts", avlSorted.checkInvariants());
        check("AVL height within 1.44 log2(n+2) on sorted input", avlSorted.height() <= AvlTree.heightBound(n));
        check("AVL size counts distinct keys", avlSorted.size() == n);
        check("AVL inorder is sorted", isSorted(avlSorted.keys()));
        avlSorted.put(5, 500);
        check("AVL put on existing key replaces value, not size", avlSorted.get(5) == 500 && avlSorted.size() == n);

        List<Integer> shuffled = new ArrayList<>();
        for (int i = 0; i < n; i++) shuffled.add(i);
        Collections.shuffle(shuffled, new Random(11));
        AvlTree<Integer, Integer> avlRandom = new AvlTree<>();
        for (int k : shuffled) avlRandom.put(k, k * 2);
        check("AVL invariants hold after random inserts", avlRandom.checkInvariants());
        check("AVL get finds every key", avlRandom.get(777) == 1554 && avlRandom.get(n - 1) == 2 * (n - 1) && avlRandom.get(n) == null);

        // ----- AVL: delete -----
        for (int i = 0; i < n; i += 3) avlRandom.remove(i);
        check("AVL remove shrinks size", avlRandom.size() == n - (n + 2) / 3);
        check("AVL invariants hold after deletes", avlRandom.checkInvariants());
        check("AVL removed keys are gone, others remain", !avlRandom.containsKey(0) && !avlRandom.containsKey(999) && avlRandom.containsKey(1) && avlRandom.containsKey(1000));
        avlRandom.remove(-1);
        check("AVL remove of a missing key is a no-op", avlRandom.size() == n - (n + 2) / 3 && avlRandom.checkInvariants());
        AvlTree<Integer, String> tiny = new AvlTree<>();
        tiny.put(1, "a");
        tiny.remove(1);
        check("AVL remove last key leaves empty tree", tiny.size() == 0 && tiny.height() == 0 && tiny.get(1) == null);

        // ----- AVL: ordered operations -----
        AvlTree<Integer, String> ord = new AvlTree<>();
        for (int k : new int[]{50, 20, 70, 10, 30, 60, 80, 25, 35}) ord.put(k, "v");
        check("range query returns keys in [lo, hi] ascending", ord.range(22, 60).equals(List.of(25, 30, 35, 50, 60)));
        check("range query with no hits is empty", ord.range(81, 100).isEmpty());
        check("ceilingKey exact hit", ord.ceilingKey(30) == 30);
        check("ceilingKey rounds up to next key", ord.ceilingKey(31) == 35);
        check("ceilingKey beyond max is null", ord.ceilingKey(81) == null);

        // ----- Red-black: cases by hand -----
        RedBlackTree<Integer, String> rbCase1 = new RedBlackTree<>();
        rbCase1.put(10, "a");
        check("RB single node: root black, black height 1", rbCase1.checkInvariants() && rbCase1.blackHeight() == 1);
        rbCase1.put(5, "b"); rbCase1.put(15, "c");                  // red children under black root
        check("RB three nodes need no rotation", rbCase1.rotations() == 0 && rbCase1.checkInvariants());
        rbCase1.put(1, "d");                                         // uncle 15 is red: case 1 recolor
        check("RB case 1 recolors (uncle red), no rotation", rbCase1.rotations() == 0 && rbCase1.checkInvariants() && rbCase1.blackHeight() == 2);
        RedBlackTree<Integer, String> rbCase3 = new RedBlackTree<>();
        rbCase3.put(10, "a"); rbCase3.put(20, "b"); rbCase3.put(30, "c");   // outer grandchild: one rotation
        check("RB case 3 rotates once on sorted triple", rbCase3.rotations() == 1 && rbCase3.height() == 2 && rbCase3.checkInvariants());
        RedBlackTree<Integer, String> rbCase2 = new RedBlackTree<>();
        rbCase2.put(10, "a"); rbCase2.put(30, "c"); rbCase2.put(20, "b");   // inner grandchild: two rotations
        check("RB case 2 then 3 rotates twice on zig-zag", rbCase2.rotations() == 2 && rbCase2.height() == 2 && rbCase2.checkInvariants());

        // ----- Red-black: invariants and bound on sorted and random input -----
        RedBlackTree<Integer, Integer> rbSorted = new RedBlackTree<>();
        for (int i = 0; i < n; i++) rbSorted.put(i, i);
        check("RB invariants hold after sorted inserts", rbSorted.checkInvariants());
        check("RB height within 2 log2(n+1)", rbSorted.height() <= RedBlackTree.heightBound(n));
        check("RB is looser than AVL on sorted input", rbSorted.height() >= avlSorted.height());
        check("RB inorder is sorted and complete", isSorted(rbSorted.keys()) && rbSorted.keys().size() == n);
        RedBlackTree<Integer, Integer> rbRandom = new RedBlackTree<>();
        for (int k : shuffled) rbRandom.put(k, k);
        check("RB invariants hold after random inserts", rbRandom.checkInvariants());
        check("RB rotates less than AVL on random input", rbRandom.rotations() < avlRandom.rotations());
        rbRandom.put(42, -1);
        check("RB put on existing key replaces value", rbRandom.get(42) == -1 && rbRandom.size() == n);

        // ----- Agreement with java.util.TreeMap -----
        TreeMap<Integer, Integer> reference = new TreeMap<>();
        for (int k : shuffled) reference.put(k, k);
        check("RB and TreeMap hold the same ordered key set", rbRandom.keys().equals(new ArrayList<>(reference.keySet())));

        // ----- Invariant checker catches corruption (hand-built nodes, since the public API cannot misbalance) -----
        AvlTree.Node<Integer, String> chain = new AvlTree.Node<>(1, "a");
        chain.right = new AvlTree.Node<>(2, "b");
        chain.right.right = new AvlTree.Node<>(3, "c");
        chain.right.height = 2; chain.height = 3;
        checkThrows("checkInvariants detects an unbalanced node", IllegalStateException.class, () -> AvlTree.verify(chain, null, null));
        AvlTree.Node<Integer, String> stale = new AvlTree.Node<>(2, "b");
        stale.left = new AvlTree.Node<>(1, "a");
        stale.height = 1;                                    // should be 2
        checkThrows("checkInvariants detects a stale height", IllegalStateException.class, () -> AvlTree.verify(stale, null, null));
        AvlTree.Node<Integer, String> disordered = new AvlTree.Node<>(2, "b");
        disordered.left = new AvlTree.Node<>(5, "x");
        disordered.height = 2;
        checkThrows("checkInvariants detects BST order violation", IllegalStateException.class, () -> AvlTree.verify(disordered, null, null));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }

    static boolean isSorted(List<Integer> xs) {
        for (int i = 1; i < xs.size(); i++) if (xs.get(i - 1) >= xs.get(i)) return false;
        return true;
    }
}
