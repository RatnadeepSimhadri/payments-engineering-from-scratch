// Tests for phases/03-systems-data-structures/07-skip-lists. Run: javac -d .out *.java && java -cp .out MainTest
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
        // ----- construction -----
        checkThrows("p must be in (0,1)", IllegalArgumentException.class, () -> new SkipList<Integer, String>(16, 1.0, new Random(1)));
        checkThrows("maxLevel must be positive", IllegalArgumentException.class, () -> new SkipList<Integer, String>(0, 0.5, new Random(1)));
        check("defaultMaxLevel for 100k keys at p=0.5 is 17", SkipList.defaultMaxLevel(100_000, 0.5) == 17);
        check("defaultMaxLevel for 100k keys at p=0.25 is 9", SkipList.defaultMaxLevel(100_000, 0.25) == 9);

        // ----- basic map behaviour -----
        SkipList<Integer, String> list = new SkipList<>(16, 0.5, new Random(42));
        check("empty list: size 0, level 1, get null", list.size() == 0 && list.level() == 1 && list.get(5) == null);
        for (int k : new int[]{742, 118, 905, 367, 512, 288, 674, 59, 831, 456}) list.put(k, "m" + k);
        check("keys come out sorted", list.keys().equals(List.of(59, 118, 288, 367, 456, 512, 674, 742, 831, 905)));
        check("get finds present keys", "m512".equals(list.get(512)) && "m59".equals(list.get(59)) && "m905".equals(list.get(905)));
        check("get misses absent keys, including beyond both ends", list.get(1) == null && list.get(500) == null && list.get(1000) == null);
        list.put(512, "updated");
        check("put on an existing key replaces the value without growing", "updated".equals(list.get(512)) && list.size() == 10);
        check("invariants hold after inserts", list.checkInvariants());

        // ----- rank and byRank (Redis ZRANK / ZRANGE by index) -----
        check("rank is 1-based position in sorted order", list.rank(59) == 1 && list.rank(512) == 6 && list.rank(905) == 10);
        check("rank of absent key is 0", list.rank(500) == 0 && list.rank(0) == 0);
        check("byRank inverts rank", list.byRank(1) == 59 && list.byRank(6) == 512 && list.byRank(10) == 905);
        check("byRank out of range is null", list.byRank(0) == null && list.byRank(11) == null);
        boolean rankRoundTrip = true;
        for (int r = 1; r <= 10; r++) rankRoundTrip &= list.rank(list.byRank(r)) == r;
        check("rank(byRank(r)) == r for every r", rankRoundTrip);

        // ----- range -----
        check("range is inclusive and ordered", list.range(300, 700).equals(List.of(367, 456, 512, 674)));
        check("range with bounds on keys includes them", list.range(118, 288).equals(List.of(118, 288)));
        check("range with no hits is empty", list.range(900, 904).isEmpty() && list.range(1000, 2000).isEmpty());

        // ----- remove -----
        check("remove returns true and drops the key", list.remove(512) && list.get(512) == null && list.size() == 9);
        check("remove of absent key returns false", !list.remove(512) && !list.remove(12345) && list.size() == 9);
        check("ranks shift down after removal", list.rank(674) == 6 && list.byRank(6) == 674);
        check("invariants hold after removal", list.checkInvariants());
        SkipList<Integer, String> one = new SkipList<>(16, 0.5, new Random(3));
        one.put(1, "a");
        one.remove(1);
        check("removing the last key returns to the empty shape", one.size() == 0 && one.level() == 1 && one.keys().isEmpty() && one.checkInvariants());

        // ----- large randomized comparison with TreeMap -----
        int n = 50_000;
        Random rng = new Random(7);
        SkipList<Integer, Integer> big = new SkipList<>(SkipList.defaultMaxLevel(n, 0.5), 0.5, rng);
        TreeMap<Integer, Integer> model = new TreeMap<>();
        List<Integer> keys = new ArrayList<>();
        for (int i = 0; i < n; i++) keys.add(i * 3);
        Collections.shuffle(keys, rng);
        for (int k : keys) { big.put(k, k); model.put(k, k); }
        check("large insert agrees with TreeMap", big.keys().equals(new ArrayList<>(model.keySet())) && big.size() == n);
        check("large insert keeps invariants", big.checkInvariants());
        boolean ranksAgree = true;
        for (int i = 0; i < 1_000; i++) {
            int k = keys.get(i);
            ranksAgree &= big.rank(k) == model.headMap(k, true).size();
        }
        check("rank agrees with TreeMap headMap size on 1000 samples", ranksAgree);
        check("range agrees with TreeMap subMap", big.range(3_000, 3_300).equals(new ArrayList<>(model.subMap(3_000, true, 3_300, true).keySet())));
        for (int i = 0; i < n; i += 2) { big.remove(keys.get(i)); model.remove(keys.get(i)); }
        check("mixed removes agree with TreeMap", big.keys().equals(new ArrayList<>(model.keySet())) && big.checkInvariants());
        check("byRank after removes matches TreeMap order", big.byRank(1).equals(model.firstKey()) && big.byRank(big.size()).equals(model.lastKey()));

        // ----- the probabilistic shape -----
        int[] hist = big.levelHistogram();
        check("level 1 holds about half the nodes", hist[1] > 0.45 * big.size() && hist[1] < 0.55 * big.size());
        check("level 2 holds about a quarter", hist[2] > 0.20 * big.size() && hist[2] < 0.30 * big.size());
        check("levels in use are within maxLevel", big.level() <= big.maxLevel && big.level() >= 10);

        // ----- expected O(log n): measured comparisons vs Pugh -----
        big.searchSteps = 0;
        for (int i = 0; i < 5_000; i++) big.get(rng.nextInt(3 * n));
        double avg = big.searchSteps / 5_000.0;
        check("average comparisons within 1.5x of log_(1/p)(n)/p", avg < 1.5 * SkipList.expectedSteps(big.size(), 0.5) && avg > 0.5 * SkipList.expectedSteps(big.size(), 0.5));

        // sorted input does not degrade: shape depends on coins, not keys
        SkipList<Integer, Integer> sortedIn = new SkipList<>(17, 0.5, new Random(11));
        for (int i = 0; i < n; i++) sortedIn.put(i, i);
        sortedIn.searchSteps = 0;
        for (int i = 0; i < 5_000; i++) sortedIn.get(i * 7 % n);
        check("sorted insertion order gives the same logarithmic cost", sortedIn.searchSteps / 5_000.0 < 1.5 * SkipList.expectedSteps(n, 0.5));

        // ----- the failure the lesson warns about: a biased coin -----
        SkipList<Integer, Integer> flat = new SkipList<>(16, 0.5, () -> 1);
        for (int i = 0; i < 2_000; i++) flat.put(i, i);
        flat.searchSteps = 0;
        for (int i = 0; i < 100; i++) flat.get(1_999);
        check("a coin that never promotes yields a linked list: O(n) search", flat.level() == 1 && flat.searchSteps / 100.0 > 1_900);
        check("the degenerate list is still a correct map", flat.get(1_234) == 1_234 && flat.rank(1_234) == 1_235 && flat.checkInvariants());

        // ----- determinism: same seed, same shape -----
        SkipList<Integer, Integer> a = new SkipList<>(16, 0.5, new Random(99)), b = new SkipList<>(16, 0.5, new Random(99));
        for (int i = 0; i < 1_000; i++) { a.put(i, i); b.put(i, i); }
        check("identical seeds produce identical level histograms", java.util.Arrays.equals(a.levelHistogram(), b.levelHistogram()));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
