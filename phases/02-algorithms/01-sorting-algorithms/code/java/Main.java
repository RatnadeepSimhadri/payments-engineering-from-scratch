// Lesson: phases/02-algorithms/01-sorting-algorithms/docs/en.md
// Sorting: Merge, Quick, Heap, and Why Libraries Use Timsort
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Knuth, TAOCP Vol. 3 (sorting stability, heapsort, quicksort analysis); Bentley & McIlroy,
// "Engineering a Sort Function" (1993, three-way partition); Peters, "Timsort" (CPython listsort.txt).
// Java 21, standard library only.

import java.util.ArrayList;
import java.util.Arrays;
import java.util.Comparator;
import java.util.List;
import java.util.Objects;
import java.util.Random;

/** ISO 4217 currency: a three-letter code and the number of decimal places in its minor unit. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);

    Currency {
        if (code == null || code.length() != 3) throw new IllegalArgumentException("currency code: " + code);
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4: " + exponent);
    }
}

/** An exact amount of one currency as a signed count of minor units (Phase 10, Lesson 01). */
record Money(long minorUnits, Currency currency) implements Comparable<Money> {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    @Override public int compareTo(Money other) {
        if (!currency.equals(other.currency)) throw new IllegalArgumentException("cannot compare " + currency.code() + " with " + other.currency.code());
        return Long.compare(minorUnits, other.minorUnits);
    }

    @Override public String toString() {
        long scale = 1;
        for (int i = 0; i < currency.exponent(); i++) scale *= 10;
        long whole = Math.abs(minorUnits) / scale, frac = Math.abs(minorUnits) % scale;
        String sign = minorUnits < 0 ? "-" : "";
        if (currency.exponent() == 0) return sign + whole + " " + currency.code();
        return String.format("%s%d.%0" + currency.exponent() + "d %s", sign, whole, frac, currency.code());
    }
}

/** A ledger line: the sequence number is the arrival order, which a stable sort by amount must preserve. */
record Transaction(long seq, String account, Money amount) {
    static final Comparator<Transaction> BY_AMOUNT = Comparator.comparing(Transaction::amount);
    static final Comparator<Transaction> BY_SEQ = Comparator.comparingLong(Transaction::seq);

    @Override public String toString() { return "#" + seq + " " + account + " " + amount; }
}

/** Wraps a comparator and counts how many times it is consulted. */
final class CountingComparator<T> implements Comparator<T> {
    private final Comparator<T> inner;
    long comparisons = 0;

    CountingComparator(Comparator<T> inner) { this.inner = inner; }

    @Override public int compare(T a, T b) { comparisons++; return inner.compare(a, b); }

    static <T extends Comparable<T>> CountingComparator<T> natural() { return new CountingComparator<>(Comparator.<T>naturalOrder()); }
}

/**
 * Four comparison sorts over a generic array. Each is a different point in the space of
 * (time, extra memory, stability, worst case). All sort in place except mergeSort, which uses an O(n) buffer.
 */
final class Sorts {
    private Sorts() {}

    static final int INSERTION_CUTOFF = 16;

    /** O(n^2) worst and average, O(n) on nearly-sorted input, stable, in place. The workhorse for short runs. */
    static <T> void insertionSort(T[] a, Comparator<? super T> cmp) { insertionSort(a, 0, a.length, cmp); }

    static <T> void insertionSort(T[] a, int from, int to, Comparator<? super T> cmp) {
        for (int i = from + 1; i < to; i++) {
            T key = a[i];
            int j = i - 1;
            // strict > keeps equal keys in their original order: this is what makes it stable
            while (j >= from && cmp.compare(a[j], key) > 0) {
                a[j + 1] = a[j];
                j--;
            }
            a[j + 1] = key;
        }
    }

    /** O(n log n) always, stable, O(n) extra memory. Top-down with an insertion-sort base case. */
    static <T> void mergeSort(T[] a, Comparator<? super T> cmp) {
        @SuppressWarnings("unchecked")
        T[] buffer = (T[]) new Object[a.length];
        mergeSort(a, buffer, 0, a.length, cmp);
    }

    private static <T> void mergeSort(T[] a, T[] buffer, int from, int to, Comparator<? super T> cmp) {
        if (to - from <= INSERTION_CUTOFF) { insertionSort(a, from, to, cmp); return; }
        int mid = from + (to - from) / 2;
        mergeSort(a, buffer, from, mid, cmp);
        mergeSort(a, buffer, mid, to, cmp);
        if (cmp.compare(a[mid - 1], a[mid]) <= 0) return; // halves already in order: skip the merge
        merge(a, buffer, from, mid, to, cmp);
    }

    static <T> void merge(T[] a, T[] buffer, int from, int mid, int to, Comparator<? super T> cmp) {
        System.arraycopy(a, from, buffer, from, to - from);
        int i = from, j = mid, k = from;
        while (i < mid && j < to) {
            // <= takes from the left half on ties, so the earlier element stays earlier: stability
            if (cmp.compare(buffer[i], buffer[j]) <= 0) a[k++] = buffer[i++];
            else a[k++] = buffer[j++];
        }
        while (i < mid) a[k++] = buffer[i++];
        while (j < to) a[k++] = buffer[j++];
    }

    /**
     * O(n log n) expected, O(n^2) worst case against an adversary who knows the pivot sequence, not stable,
     * O(log n) stack. Three-way (Dutch national flag) partition makes it O(n) on all-equal keys.
     */
    static <T> void quickSort(T[] a, Comparator<? super T> cmp, Random rng) {
        quickSort(a, 0, a.length - 1, cmp, rng);
    }

    private static <T> void quickSort(T[] a, int lo, int hi, Comparator<? super T> cmp, Random rng) {
        while (hi - lo + 1 > INSERTION_CUTOFF) {
            swap(a, lo, lo + rng.nextInt(hi - lo + 1));   // random pivot: no fixed input is a worst case
            T pivot = a[lo];
            int lt = lo, gt = hi, i = lo + 1;
            while (i <= gt) {
                int c = cmp.compare(a[i], pivot);
                if (c < 0) swap(a, lt++, i++);
                else if (c > 0) swap(a, i, gt--);
                else i++;
            }
            // a[lo..lt-1] < pivot, a[lt..gt] == pivot, a[gt+1..hi] > pivot
            // recurse on the smaller side, loop on the larger: stack depth stays O(log n)
            if (lt - lo < hi - gt) { quickSort(a, lo, lt - 1, cmp, rng); lo = gt + 1; }
            else { quickSort(a, gt + 1, hi, cmp, rng); hi = lt - 1; }
        }
        insertionSort(a, lo, hi + 1, cmp);
    }

    /** O(n log n) always, in place, O(1) extra memory, not stable. */
    static <T> void heapSort(T[] a, Comparator<? super T> cmp) {
        int n = a.length;
        for (int i = n / 2 - 1; i >= 0; i--) siftDown(a, i, n, cmp);   // build max-heap bottom-up: O(n)
        for (int end = n - 1; end > 0; end--) {
            swap(a, 0, end);            // largest to its final slot
            siftDown(a, 0, end, cmp);   // restore the heap on the prefix
        }
    }

    private static <T> void siftDown(T[] a, int root, int size, Comparator<? super T> cmp) {
        while (true) {
            int left = 2 * root + 1, right = left + 1, largest = root;
            if (left < size && cmp.compare(a[left], a[largest]) > 0) largest = left;
            if (right < size && cmp.compare(a[right], a[largest]) > 0) largest = right;
            if (largest == root) return;
            swap(a, root, largest);
            root = largest;
        }
    }

    static <T> void swap(T[] a, int i, int j) { T t = a[i]; a[i] = a[j]; a[j] = t; }

    /** True when a[i] <= a[i+1] for every i. */
    static <T> boolean isSorted(T[] a, Comparator<? super T> cmp) {
        for (int i = 1; i < a.length; i++) if (cmp.compare(a[i - 1], a[i]) > 0) return false;
        return true;
    }

    /**
     * True when every run of equal keys in `sorted` appears in the same relative order as in `original`.
     * The `identity` comparator must distinguish the elements completely (for example by sequence number).
     */
    static <T> boolean isStablePermutation(T[] original, T[] sorted, Comparator<? super T> key, Comparator<? super T> identity) {
        if (original.length != sorted.length || !isSorted(sorted, key)) return false;
        List<T> remaining = new ArrayList<>(Arrays.asList(original));
        for (int i = 1; i < sorted.length; i++) {
            if (key.compare(sorted[i - 1], sorted[i]) == 0) {
                int p = indexOf(remaining, sorted[i - 1], identity), q = indexOf(remaining, sorted[i], identity);
                if (p < 0 || q < 0 || p > q) return false;
            }
        }
        return true;
    }

    private static <T> int indexOf(List<T> xs, T x, Comparator<? super T> identity) {
        for (int i = 0; i < xs.size(); i++) if (identity.compare(xs.get(i), x) == 0) return i;
        return -1;
    }
}

public final class Main {
    static Transaction[] sampleLedger() {
        Currency usd = Currency.USD;
        return new Transaction[] {
            new Transaction(1, "acct-A", Money.ofMinor(1999, usd)),
            new Transaction(2, "acct-B", Money.ofMinor(500, usd)),
            new Transaction(3, "acct-C", Money.ofMinor(1999, usd)),
            new Transaction(4, "acct-A", Money.ofMinor(500, usd)),
            new Transaction(5, "acct-D", Money.ofMinor(12000, usd)),
            new Transaction(6, "acct-B", Money.ofMinor(1999, usd)),
            new Transaction(7, "acct-E", Money.ofMinor(500, usd)),
            new Transaction(8, "acct-C", Money.ofMinor(75, usd)),
        };
    }

    static Integer[] randomInts(int n, int bound, Random rng) {
        Integer[] a = new Integer[n];
        for (int i = 0; i < n; i++) a[i] = rng.nextInt(bound);
        return a;
    }

    public static void main(String[] args) {
        System.out.println("== Stable vs unstable: sort a ledger by amount, keep arrival order inside each amount ==");
        Transaction[] original = sampleLedger();
        Transaction[] stable = original.clone();
        Sorts.mergeSort(stable, Transaction.BY_AMOUNT);
        System.out.println("mergeSort (stable):");
        for (Transaction t : stable) System.out.println("  " + t);
        Transaction[] unstable = original.clone();
        Sorts.heapSort(unstable, Transaction.BY_AMOUNT);
        System.out.println("heapSort (not stable):");
        for (Transaction t : unstable) System.out.println("  " + t);
        System.out.println("merge preserves seq order within equal amounts: "
                + Sorts.isStablePermutation(original, stable, Transaction.BY_AMOUNT, Transaction.BY_SEQ));
        System.out.println("heap preserves seq order within equal amounts:  "
                + Sorts.isStablePermutation(original, unstable, Transaction.BY_AMOUNT, Transaction.BY_SEQ));
        Transaction[] fixed = original.clone();
        Sorts.heapSort(fixed, Transaction.BY_AMOUNT.thenComparing(Transaction.BY_SEQ));
        System.out.println("heapSort with a (amount, seq) tie-break is deterministic: "
                + Sorts.isStablePermutation(original, fixed, Transaction.BY_AMOUNT, Transaction.BY_SEQ));

        System.out.println();
        System.out.println("== Comparison counts, n = 4096 random ints, seed 42 ==");
        Random rng = new Random(42);
        Integer[] data = randomInts(4096, 1_000_000, rng);
        String[] names = {"insertion", "merge", "quick(3-way)", "heap"};
        for (String name : names) {
            Integer[] copy = data.clone();
            CountingComparator<Integer> cc = CountingComparator.natural();
            switch (name) {
                case "insertion" -> Sorts.insertionSort(copy, cc);
                case "merge" -> Sorts.mergeSort(copy, cc);
                case "quick(3-way)" -> Sorts.quickSort(copy, cc, new Random(7));
                default -> Sorts.heapSort(copy, cc);
            }
            System.out.printf("  %-13s %,10d comparisons  sorted=%b%n", name, cc.comparisons, Sorts.isSorted(copy, cc));
        }
        System.out.printf("  n log2 n       %,10d%n", Math.round(4096 * (Math.log(4096) / Math.log(2))));

        System.out.println();
        System.out.println("== Duplicate-heavy input: 4096 values drawn from only 3 keys ==");
        Integer[] dupes = randomInts(4096, 3, new Random(1));
        CountingComparator<Integer> q3 = CountingComparator.natural();
        Sorts.quickSort(dupes.clone(), q3, new Random(7));
        CountingComparator<Integer> mg = CountingComparator.natural();
        Sorts.mergeSort(dupes.clone(), mg);
        System.out.printf("  quick(3-way)  %,8d comparisons (three-way partition finishes equal keys in one pass)%n", q3.comparisons);
        System.out.printf("  merge         %,8d comparisons%n", mg.comparisons);

        System.out.println();
        System.out.println("== Nearly sorted input: 4096 ascending ints with 8 adjacent swaps ==");
        Integer[] nearly = new Integer[4096];
        for (int i = 0; i < nearly.length; i++) nearly[i] = i;
        Random swapper = new Random(3);
        for (int k = 0; k < 8; k++) { int i = swapper.nextInt(4095); Sorts.swap(nearly, i, i + 1); }   // 8 adjacent swaps
        CountingComparator<Integer> ins = CountingComparator.natural();
        Sorts.insertionSort(nearly.clone(), ins);
        CountingComparator<Integer> hp = CountingComparator.natural();
        Sorts.heapSort(nearly.clone(), hp);
        System.out.printf("  insertion     %,8d comparisons (adaptive: close to n)%n", ins.comparisons);
        System.out.printf("  heap          %,8d comparisons (oblivious: always n log n)%n", hp.comparisons);
        System.out.println();
        System.out.println("Library rule of thumb: Arrays.sort(Object[]) is Timsort (stable, adaptive, O(n) buffer);");
        System.out.println("Arrays.sort(int[]) is dual-pivot quicksort (unstable, in place) because primitives have no identity to preserve.");
    }
}
