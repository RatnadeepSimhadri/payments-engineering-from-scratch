// Tests for phases/01-complexity-and-core-structures/08-binary-search-trees. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
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

    static List<Integer> keys(BinarySearchTree<Integer, ?> t) {
        List<Integer> out = new ArrayList<>();
        for (Entry<Integer, ?> e : t.inOrder()) out.add(e.key());
        return out;
    }

    static BinarySearchTree<Integer, String> sample() {
        BinarySearchTree<Integer, String> t = new BinarySearchTree<>();
        for (int k : new int[] {50, 30, 70, 20, 40, 60, 80, 35, 45}) t.put(k, "v" + k);
        return t;
    }

    public static void main(String[] args) {
        // empty tree boundaries
        BinarySearchTree<Integer, String> empty = new BinarySearchTree<>();
        check("empty size", empty.size() == 0 && empty.isEmpty());
        check("empty height is -1", empty.height() == -1);
        check("empty min/max null", empty.minKey() == null && empty.maxKey() == null);
        check("empty get null", empty.get(1) == null);
        check("empty remove null", empty.remove(1) == null);
        check("empty range empty", empty.rangeQuery(0, 10).isEmpty());
        check("empty invariant", empty.checkInvariant());

        // insert, search, order
        BinarySearchTree<Integer, String> t = sample();
        check("size after 9 puts", t.size() == 9);
        check("in-order is sorted", keys(t).equals(List.of(20, 30, 35, 40, 45, 50, 60, 70, 80)));
        check("height of sample", t.height() == 3);
        check("get hit", "v45".equals(t.get(45)));
        check("get miss", t.get(55) == null);
        check("containsKey", t.containsKey(80) && !t.containsKey(81));
        check("min/max", t.minKey() == 20 && t.maxKey() == 80);
        check("put replaces and returns old", "v40".equals(t.put(40, "new")) && "new".equals(t.get(40)) && t.size() == 9);
        check("invariant after puts", t.checkInvariant());
        checkThrows("null key rejected", NullPointerException.class, () -> t.put(null, "x"));

        // floor and ceiling
        check("floor exact", t.floorKey(45) == 45);
        check("floor between", t.floorKey(42) == 40);
        check("floor below min is null", t.floorKey(10) == null);
        check("ceiling between", t.ceilingKey(42) == 45);
        check("ceiling above max is null", t.ceilingKey(90) == null);

        // delete: leaf, one child, two children, root
        BinarySearchTree<Integer, String> d = sample();
        check("remove leaf", "v20".equals(d.remove(20)) && keys(d).equals(List.of(30, 35, 40, 45, 50, 60, 70, 80)));
        check("remove missing returns null and keeps size", d.remove(999) == null && d.size() == 8);
        check("remove node with two children uses successor", "v30".equals(d.remove(30)) && keys(d).equals(List.of(35, 40, 45, 50, 60, 70, 80)) && d.checkInvariant());
        check("remove root with two children", "v50".equals(d.remove(50)) && keys(d).equals(List.of(35, 40, 45, 60, 70, 80)) && d.checkInvariant());
        check("remove node with one child", "v70".equals(d.remove(70)) && keys(d).equals(List.of(35, 40, 45, 60, 80)) && d.checkInvariant());
        check("successor key still reachable", "v60".equals(d.get(60)) && "v45".equals(d.get(45)));
        for (int k : List.of(35, 40, 45, 60, 80)) d.remove(k);
        check("remove everything leaves empty tree", d.isEmpty() && d.height() == -1 && d.checkInvariant());

        // range query: inclusive bounds, pruning, empty, invalid
        BinarySearchTree<Integer, String> r = sample();
        List<Integer> inRange = new ArrayList<>();
        for (Entry<Integer, String> e : r.rangeQuery(35, 60)) inRange.add(e.key());
        check("range inclusive on both ends", inRange.equals(List.of(35, 40, 45, 50, 60)));
        check("range with no hits", r.rangeQuery(81, 100).isEmpty() && r.rangeQuery(0, 19).isEmpty());
        check("range single point", r.rangeQuery(70, 70).size() == 1);
        check("range covering everything", r.rangeQuery(Integer.MIN_VALUE, Integer.MAX_VALUE).size() == 9);
        checkThrows("range lo > hi rejected", IllegalArgumentException.class, () -> r.rangeQuery(60, 35));
        r.resetComparisons();
        r.rangeQuery(35, 45);
        check("range query prunes: fewer comparisons than a full scan", r.comparisons() < 3 * 9);

        // randomized cross-check against java.util.TreeMap
        Random rnd = new Random(2024);
        BinarySearchTree<Integer, Integer> mine = new BinarySearchTree<>();
        TreeMap<Integer, Integer> ref = new TreeMap<>();
        boolean agree = true;
        for (int i = 0; i < 3_000 && agree; i++) {
            int k = rnd.nextInt(500);
            if (rnd.nextInt(3) == 0) {
                agree = java.util.Objects.equals(mine.remove(k), ref.remove(k));
            } else {
                agree = java.util.Objects.equals(mine.put(k, i), ref.put(k, i));
            }
            if (i % 250 == 0) {
                int lo = rnd.nextInt(500), hi = lo + rnd.nextInt(100);
                List<Integer> a = new ArrayList<>();
                for (Entry<Integer, Integer> e : mine.rangeQuery(lo, hi)) a.add(e.key());
                agree &= a.equals(new ArrayList<>(ref.subMap(lo, true, hi, true).keySet()));
                agree &= java.util.Objects.equals(mine.floorKey(lo), ref.floorKey(lo));
                agree &= java.util.Objects.equals(mine.ceilingKey(hi), ref.ceilingKey(hi));
            }
        }
        check("randomized ops agree with TreeMap", agree && mine.size() == ref.size() && mine.checkInvariant());
        check("randomized in-order matches TreeMap", keys(mine).equals(new ArrayList<>(ref.keySet())));

        // degenerate case: sorted insert order gives height n-1
        int n = 1_000;
        BinarySearchTree<Integer, Integer> chain = new BinarySearchTree<>();
        for (int i = 0; i < n; i++) chain.put(i, i);
        check("sorted inserts produce a chain of height n-1", chain.height() == n - 1);
        chain.resetComparisons();
        chain.get(n - 1);
        check("lookup in a chain costs n comparisons", chain.comparisons() == n);
        BinarySearchTree<Integer, Integer> shuffled = new BinarySearchTree<>();
        int[] ks = new int[n];
        for (int i = 0; i < n; i++) ks[i] = i;
        for (int i = n - 1; i > 0; i--) { int j = rnd.nextInt(i + 1); int tmp = ks[i]; ks[i] = ks[j]; ks[j] = tmp; }
        for (int k : ks) shuffled.put(k, k);
        check("shuffled inserts stay shallow (< 4 log2 n)", shuffled.height() < 4 * 10);
        check("chain still iterable without recursion", chain.inOrder().size() == n && chain.checkInvariant());

        // transaction index: composite keys keep same-timestamp postings, range is by timestamp
        TransactionIndex idx = new TransactionIndex();
        long t0 = 1_700_000_000_000L;
        Transaction a = idx.record("a", t0 + 1_000, Money.ofMinor(1_000, Currency.USD));
        Transaction b = idx.record("b", t0 + 1_000, Money.ofMinor(2_000, Currency.USD));
        Transaction c = idx.record("c", t0 + 5_000, Money.ofMinor(300, Currency.USD));
        idx.record("d", t0 + 9_000, Money.ofMinor(44, Currency.USD));
        check("same timestamp keeps both postings", idx.size() == 4 && idx.between(t0 + 1_000, t0 + 1_000).size() == 2);
        check("range by timestamp is oldest first", idx.between(t0, t0 + 5_000).stream().map(Transaction::id).toList().equals(List.of("a", "b", "c")));
        check("sum over window is exact Money", idx.sumBetween(t0, t0 + 5_000, Currency.USD).equals(Money.ofMinor(3_300, Currency.USD)));
        check("remove one of two same-timestamp postings", idx.remove(a) && idx.between(t0 + 1_000, t0 + 1_000).equals(List.of(b)));
        check("remove twice is false", !idx.remove(a) && idx.remove(c) && idx.size() == 2);
        check("index invariant", idx.tree().checkInvariant());
        check("money toString", Money.ofMinor(3_300, Currency.USD).toString().equals("33.00 USD"));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
