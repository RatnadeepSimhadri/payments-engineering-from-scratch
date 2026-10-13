// Tests for phases/00-setup-and-tooling/03-testing-from-the-standard-library. Run: javac -d .out *.java && java -cp .out MainTest
//
// This file IS the lesson's Java test harness. There is no JUnit on the classpath, so the harness is three
// static methods: check (a boolean), checkEquals (prints expected vs actual), and checkThrows (an exception
// type). Every later lesson copies these. main() counts failures and exits 1 when there are any, which is
// what `java -cp .out MainTest` and scripts/run_lesson.py rely on.
import java.util.ArrayList;
import java.util.List;
import java.util.Objects;
import java.util.concurrent.CountDownLatch;

public final class MainTest {
    private static int passed = 0;
    private static final List<String> failures = new ArrayList<>();

    static void check(String name, boolean condition) {
        if (condition) passed++; else failures.add(name);
    }

    static void checkEquals(String name, Object expected, Object actual) {
        if (Objects.equals(expected, actual)) passed++;
        else failures.add(name + " (expected " + expected + ", got " + actual + ")");
    }

    static void checkThrows(String name, Class<? extends Throwable> type, Runnable action) {
        try {
            action.run();
            failures.add(name + " (no exception)");
        } catch (Throwable t) {
            if (type.isInstance(t)) passed++;
            else failures.add(name + " (wrong exception " + t.getClass().getSimpleName() + ")");
        }
    }

    public static void main(String[] args) throws InterruptedException {
        // --- SeededRandom: the sequence is the contract, identical in all three languages ---
        SeededRandom rng = new SeededRandom(42);
        checkEquals("seed 42 #1", -4767286540954276203L, rng.nextLong());
        checkEquals("seed 42 #2", 2949826092126892291L, rng.nextLong());
        checkEquals("seed 42 #3", 5139283748462763858L, rng.nextLong());
        checkEquals("seed 42 #4", 6349198060258255764L, rng.nextLong());
        checkEquals("seed 42 #5", 701532786141963250L, rng.nextLong());
        SeededRandom reference = new SeededRandom(1234567);          // published SplitMix64 vector
        checkEquals("SplitMix64 reference vector #1", 6457827717110365317L, reference.nextLong());
        checkEquals("SplitMix64 reference vector #2", 3203168211198807973L, reference.nextLong());
        check("same seed, same sequence", new SeededRandom(99).nextLong() == new SeededRandom(99).nextLong());
        check("different seed, different sequence", new SeededRandom(1).nextLong() != new SeededRandom(2).nextLong());
        checkEquals("seed is remembered", 42L, new SeededRandom(42).seed());

        rng = new SeededRandom(42);
        int[] expectedInts = {51, 53, 71, 35, 65, 97, 82, 63};
        boolean intsMatch = true;
        for (int e : expectedInts) intsMatch &= rng.nextInt(100) == e;
        check("nextInt(100) sequence for seed 42", intsMatch);
        rng = new SeededRandom(42);
        boolean inRange = true;
        for (int i = 0; i < 10_000; i++) { int v = rng.nextInt(7); inRange &= v >= 0 && v < 7; }
        check("nextInt stays in [0, bound)", inRange);
        checkThrows("nextInt rejects non-positive bound", IllegalArgumentException.class, () -> new SeededRandom(1).nextInt(0));
        rng = new SeededRandom(42);
        boolean doublesOk = true;
        for (int i = 0; i < 10_000; i++) { double d = rng.nextDouble(); doublesOk &= d >= 0.0 && d < 1.0; }
        check("nextDouble stays in [0, 1)", doublesOk);
        check("nextDouble first value for seed 42", Math.abs(new SeededRandom(42).nextDouble() - 0.7415648787718233) < 1e-15);

        List<Integer> deck = new ArrayList<>(List.of(0, 1, 2, 3, 4, 5, 6, 7, 8, 9));
        new SeededRandom(7).shuffle(deck);
        checkEquals("shuffle seed 7 is deterministic and the same in every language", List.of(3, 2, 1, 6, 7, 5, 4, 8, 9, 0), deck);
        List<Integer> sorted = new ArrayList<>(deck);
        sorted.sort(null);
        checkEquals("shuffle is a permutation", List.of(0, 1, 2, 3, 4, 5, 6, 7, 8, 9), sorted);

        // --- FakeClock: time moves only when told, never backwards ---
        FakeClock clock = new FakeClock(1_000);
        checkEquals("starts where told", 1_000L, clock.nowMillis());
        clock.advance(500);
        checkEquals("advance adds", 1_500L, clock.nowMillis());
        clock.advance(0);
        checkEquals("advance by zero is allowed", 1_500L, clock.nowMillis());
        clock.advanceTo(2_000);
        checkEquals("advanceTo sets", 2_000L, clock.nowMillis());
        clock.advanceTo(2_000);
        checkEquals("advanceTo same instant is allowed", 2_000L, clock.nowMillis());
        checkThrows("advance rejects negative", IllegalArgumentException.class, () -> clock.advance(-1));
        checkThrows("advanceTo rejects the past", IllegalArgumentException.class, () -> clock.advanceTo(1_999));
        checkEquals("rejected moves leave the clock untouched", 2_000L, clock.nowMillis());
        check("two fake clocks are independent", new FakeClock(5).nowMillis() == 5 && new FakeClock(6).nowMillis() == 6);

        // thread-safety: 8 threads advance by 1, 1000 times each; the invariant is the total
        FakeClock shared = new FakeClock(0);
        int threads = 8, perThread = 1_000;
        CountDownLatch done = new CountDownLatch(threads);
        for (int t = 0; t < threads; t++) {
            Thread.ofPlatform().start(() -> {
                for (int i = 0; i < perThread; i++) shared.advance(1);
                done.countDown();
            });
        }
        done.await();
        checkEquals("concurrent advances are not lost", (long) threads * perThread, shared.nowMillis());

        // --- Deadline: the consumer that shows why the clock is injected ---
        FakeClock c2 = new FakeClock(10_000);
        Deadline d = new Deadline(c2, 10_000 + 30_000);
        check("not expired at start", !d.expired());
        checkEquals("remaining at start", 30_000L, d.remainingMillis());
        c2.advance(29_999);
        check("not expired one ms before", !d.expired());
        c2.advance(1);
        check("expired exactly at the deadline (>=)", d.expired());
        c2.advance(5_000);
        checkEquals("remaining never negative", 0L, d.remainingMillis());

        // --- Backoff: deterministic given the rng, bounded by the window ---
        SeededRandom b = new SeededRandom(42);
        long[] expectedWaits = {51, 153, 171, 735, 365};
        boolean waitsMatch = true;
        for (int attempt = 0; attempt < 5; attempt++) waitsMatch &= Backoff.backoffMillis(attempt, 100, 2000, b) == expectedWaits[attempt];
        check("backoff sequence for seed 42", waitsMatch);
        b = new SeededRandom(3);
        boolean capped = true;
        for (int attempt = 0; attempt < 200; attempt++) capped &= Backoff.backoffMillis(attempt, 100, 2000, b) < 2000;
        check("backoff never exceeds the cap, even at attempt 199", capped);
        boolean windowed = true;
        b = new SeededRandom(5);
        for (int i = 0; i < 1_000; i++) windowed &= Backoff.backoffMillis(0, 100, 2000, b) < 100;
        check("attempt 0 stays inside [0, base)", windowed);
        checkThrows("backoff rejects negative attempt", IllegalArgumentException.class, () -> Backoff.backoffMillis(-1, 100, 2000, new SeededRandom(1)));
        checkThrows("backoff rejects cap below base", IllegalArgumentException.class, () -> Backoff.backoffMillis(0, 100, 50, new SeededRandom(1)));

        // --- the harness itself: checkThrows must notice a missing exception ---
        int before = failures.size();
        checkThrows("harness self-test (expected to be recorded as a failure)", IllegalStateException.class, () -> {});
        boolean noticed = failures.size() == before + 1;
        failures.remove(failures.size() - 1);                    // undo the deliberate failure
        check("checkThrows records a missing exception as a failure", noticed);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
