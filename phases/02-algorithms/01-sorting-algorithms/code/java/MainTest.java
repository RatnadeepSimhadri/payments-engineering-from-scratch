// Tests for phases/02-algorithms/01-sorting-algorithms. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Comparator;
import java.util.List;
import java.util.Random;

public final class MainTest {
    private static int passed = 0;
    private static final List<String> failures = new ArrayList<>();

    static void check(String name, boolean condition) {
        if (condition) passed++; else failures.add(name);
    }

    interface Sorter { <T> void sort(T[] a, Comparator<? super T> cmp); }

    static final Sorter INSERTION = new Sorter() { public <T> void sort(T[] a, Comparator<? super T> c) { Sorts.insertionSort(a, c); } };
    static final Sorter MERGE = new Sorter() { public <T> void sort(T[] a, Comparator<? super T> c) { Sorts.mergeSort(a, c); } };
    static final Sorter QUICK = new Sorter() { public <T> void sort(T[] a, Comparator<? super T> c) { Sorts.quickSort(a, c, new Random(99)); } };
    static final Sorter HEAP = new Sorter() { public <T> void sort(T[] a, Comparator<? super T> c) { Sorts.heapSort(a, c); } };
    static final String[] NAMES = {"insertion", "merge", "quick", "heap"};
    static final Sorter[] ALL = {INSERTION, MERGE, QUICK, HEAP};

    /** The reference: every sort must produce exactly what the JDK's stable sort produces, as a multiset. */
    static boolean sameMultisetAndSorted(Integer[] input, Integer[] output) {
        Integer[] expected = input.clone();
        Arrays.sort(expected);
        return Arrays.equals(expected, output);
    }

    public static void main(String[] args) {
        Random rng = new Random(2024);
        Comparator<Integer> nat = Comparator.naturalOrder();

        // happy path: random arrays of several sizes, every algorithm
        for (int k = 0; k < ALL.length; k++) {
            for (int n : new int[] {0, 1, 2, 15, 16, 17, 100, 1000}) {
                Integer[] a = Main.randomInts(n, 50, rng);
                Integer[] out = a.clone();
                ALL[k].sort(out, nat);
                check(NAMES[k] + " sorts n=" + n, sameMultisetAndSorted(a, out));
            }
        }

        // boundaries: already sorted, reverse sorted, all equal
        Integer[] asc = new Integer[500], desc = new Integer[500], same = new Integer[500];
        for (int i = 0; i < 500; i++) { asc[i] = i; desc[i] = 499 - i; same[i] = 7; }
        for (int k = 0; k < ALL.length; k++) {
            Integer[] a = asc.clone(), d = desc.clone(), s = same.clone();
            ALL[k].sort(a, nat); ALL[k].sort(d, nat); ALL[k].sort(s, nat);
            check(NAMES[k] + " ascending input", Arrays.equals(a, asc));
            check(NAMES[k] + " descending input", Arrays.equals(d, asc));
            check(NAMES[k] + " all-equal input", Arrays.equals(s, same));
        }

        // stability: insertion and merge keep arrival order inside equal amounts; heap and quick do not promise it
        Transaction[] ledger = Main.sampleLedger();
        Transaction[] byInsertion = ledger.clone(); Sorts.insertionSort(byInsertion, Transaction.BY_AMOUNT);
        Transaction[] byMerge = ledger.clone(); Sorts.mergeSort(byMerge, Transaction.BY_AMOUNT);
        Transaction[] byHeap = ledger.clone(); Sorts.heapSort(byHeap, Transaction.BY_AMOUNT);
        check("insertion sort is stable on ledger", Sorts.isStablePermutation(ledger, byInsertion, Transaction.BY_AMOUNT, Transaction.BY_SEQ));
        check("merge sort is stable on ledger", Sorts.isStablePermutation(ledger, byMerge, Transaction.BY_AMOUNT, Transaction.BY_SEQ));
        check("merge sort output order", byMerge[0].seq() == 8 && byMerge[1].seq() == 2 && byMerge[2].seq() == 4 && byMerge[3].seq() == 7
                && byMerge[4].seq() == 1 && byMerge[5].seq() == 3 && byMerge[6].seq() == 6 && byMerge[7].seq() == 5);
        check("heap sort is sorted by amount", Sorts.isSorted(byHeap, Transaction.BY_AMOUNT));
        check("heap sort breaks arrival order on this ledger", !Sorts.isStablePermutation(ledger, byHeap, Transaction.BY_AMOUNT, Transaction.BY_SEQ));

        // stability at scale: 2000 records with only 5 distinct keys, merge must be stable, every time
        Transaction[] big = new Transaction[2000];
        for (int i = 0; i < big.length; i++) big[i] = new Transaction(i, "a", Money.ofMinor(rng.nextInt(5) * 100, Currency.USD));
        Transaction[] bigMerge = big.clone(); Sorts.mergeSort(bigMerge, Transaction.BY_AMOUNT);
        check("merge sort stable on 2000 records / 5 keys", Sorts.isStablePermutation(big, bigMerge, Transaction.BY_AMOUNT, Transaction.BY_SEQ));
        Transaction[] bigQuick = big.clone(); Sorts.quickSort(bigQuick, Transaction.BY_AMOUNT, new Random(5));
        check("quick sort sorted on 2000 records / 5 keys", Sorts.isSorted(bigQuick, Transaction.BY_AMOUNT));
        check("quick sort is not stable on 2000 records / 5 keys", !Sorts.isStablePermutation(big, bigQuick, Transaction.BY_AMOUNT, Transaction.BY_SEQ));

        // the fix for an unstable sort: make the key total by appending the arrival sequence
        Transaction[] bigHeapFixed = big.clone();
        Sorts.heapSort(bigHeapFixed, Transaction.BY_AMOUNT.thenComparing(Transaction.BY_SEQ));
        check("unstable sort + (amount, seq) key reproduces the stable order", Arrays.equals(bigHeapFixed, bigMerge));

        // complexity evidence: comparison counts
        Integer[] data = Main.randomInts(4096, 1_000_000, new Random(42));
        CountingComparator<Integer> cIns = CountingComparator.natural(); Sorts.insertionSort(data.clone(), cIns);
        CountingComparator<Integer> cMerge = CountingComparator.natural(); Sorts.mergeSort(data.clone(), cMerge);
        CountingComparator<Integer> cQuick = CountingComparator.natural(); Sorts.quickSort(data.clone(), cQuick, new Random(7));
        CountingComparator<Integer> cHeap = CountingComparator.natural(); Sorts.heapSort(data.clone(), cHeap);
        long nlogn = Math.round(4096 * Math.log(4096) / Math.log(2));   // 49,152
        check("insertion is quadratic: > n^2/8 comparisons", cIns.comparisons > 4096L * 4096 / 8);
        check("merge sort within 1.1 n log n comparisons", cMerge.comparisons <= 1.1 * nlogn);
        check("quick sort within 2.0 n log n comparisons", cQuick.comparisons <= 2 * nlogn);
        check("heap sort within 2.2 n log n comparisons", cHeap.comparisons <= 2.2 * nlogn);
        check("heap sort uses more comparisons than merge sort", cHeap.comparisons > cMerge.comparisons);

        // three-way partition: all-equal input is linear, not quadratic
        Integer[] equal = new Integer[4096]; Arrays.fill(equal, 5);
        CountingComparator<Integer> cEq = CountingComparator.natural(); Sorts.quickSort(equal, cEq, new Random(7));
        check("3-way quicksort on all-equal keys is linear (< 3n comparisons)", cEq.comparisons < 3 * 4096);

        // adaptivity: insertion sort on nearly sorted input is close to linear; heap sort is not
        Integer[] nearly = new Integer[4096];
        for (int i = 0; i < nearly.length; i++) nearly[i] = i;
        Sorts.swap(nearly, 10, 20); Sorts.swap(nearly, 3000, 3001);
        CountingComparator<Integer> cNear = CountingComparator.natural(); Sorts.insertionSort(nearly.clone(), cNear);
        CountingComparator<Integer> cNearHeap = CountingComparator.natural(); Sorts.heapSort(nearly.clone(), cNearHeap);
        check("insertion sort adaptive on nearly-sorted input (< 2n comparisons)", cNear.comparisons < 2 * 4096);
        check("heap sort oblivious on nearly-sorted input (> n log n / 2)", cNearHeap.comparisons > nlogn / 2);

        // worst case for a fixed-pivot quicksort is defeated by the random pivot: sorted input stays n log n
        Integer[] sortedBig = new Integer[8192];
        for (int i = 0; i < sortedBig.length; i++) sortedBig[i] = i;
        CountingComparator<Integer> cSortedQuick = CountingComparator.natural(); Sorts.quickSort(sortedBig, cSortedQuick, new Random(11));
        check("random-pivot quicksort on sorted input is not quadratic", cSortedQuick.comparisons < 8192L * 8192 / 50);

        // merge helper and the isStablePermutation checker itself
        Integer[] halves = {1, 4, 7, 2, 3, 9};
        Sorts.merge(halves, new Integer[6], 0, 3, 6, nat);
        check("merge of two sorted halves", Arrays.equals(halves, new Integer[] {1, 2, 3, 4, 7, 9}));
        Transaction[] unsorted = ledger.clone();
        check("isStablePermutation rejects an unsorted array", !Sorts.isStablePermutation(ledger, unsorted, Transaction.BY_AMOUNT, Transaction.BY_SEQ));

        // Money guards: comparing across currencies is a programming error
        boolean threw = false;
        try { Money.ofMinor(1, Currency.USD).compareTo(Money.ofMinor(1, new Currency("EUR", 2))); } catch (IllegalArgumentException e) { threw = true; }
        check("Money.compareTo rejects currency mismatch", threw);
        check("Money toString", Money.ofMinor(1999, Currency.USD).toString().equals("19.99 USD"));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
