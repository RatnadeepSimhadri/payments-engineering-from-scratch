// Lesson: phases/00-setup-and-tooling/03-testing-from-the-standard-library/docs/en.md
// Testing from the Standard Library: a check() harness, FakeClock, SeededRandom, and a jittered backoff to test with them
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Steele, Lea, Flood, "Fast Splittable Pseudorandom Number Generators" (OOPSLA 2014) for SplitMix64;
//          Brooker, "Exponential Backoff And Jitter" (AWS Architecture Blog, 2015) for full-jitter backoff.
// Java 21, standard library only. Same design as the Python and Go versions.

import java.util.ArrayList;
import java.util.List;

/** A source of time in epoch milliseconds. Production code takes a Clock; tests hand it a FakeClock. */
interface Clock {
    long nowMillis();
}

/** The real clock. Never appears in a test. */
final class SystemClock implements Clock {
    @Override
    public long nowMillis() {
        return System.currentTimeMillis();
    }
}

/**
 * A clock that moves only when told to. Thread-safe so code under test may read it from worker threads.
 * Invariant: time never goes backwards; advance() rejects negatives and advanceTo() rejects the past.
 */
final class FakeClock implements Clock {
    private long now;

    FakeClock(long startMillis) {
        this.now = startMillis;
    }

    @Override
    public synchronized long nowMillis() {
        return now;
    }

    synchronized void advance(long millis) {
        if (millis < 0) throw new IllegalArgumentException("cannot advance by a negative duration: " + millis);
        now = Math.addExact(now, millis);
    }

    synchronized void advanceTo(long millis) {
        if (millis < now) throw new IllegalArgumentException("cannot move the clock backwards from " + now + " to " + millis);
        now = millis;
    }
}

/**
 * SplitMix64: a 64-bit generator with a 64-bit state. Ten lines, and the same sequence for the same
 * seed in Java, Python, and Go, which is why the course uses it instead of each language's own PRNG.
 * Not for cryptography or anything money-bearing; for simulations, shuffles, and jitter.
 */
final class SeededRandom {
    private static final long GOLDEN_GAMMA = 0x9E3779B97F4A7C15L;
    private final long seed;
    private long state;

    SeededRandom(long seed) {
        this.seed = seed;
        this.state = seed;
    }

    long seed() {
        return seed;
    }

    /** The next 64 bits, as a signed long. */
    long nextLong() {
        state += GOLDEN_GAMMA;
        long z = state;
        z = (z ^ (z >>> 30)) * 0xBF58476D1CE4E5B9L;
        z = (z ^ (z >>> 27)) * 0x94D049BB133111EBL;
        return z ^ (z >>> 31);
    }

    /** Uniform in [0, bound). Uses the top 31 bits; the modulo bias is below 2^-31 for any realistic bound. */
    int nextInt(int bound) {
        if (bound <= 0) throw new IllegalArgumentException("bound must be positive: " + bound);
        return (int) ((nextLong() >>> 33) % bound);
    }

    /** Uniform in [0.0, 1.0): the top 53 bits scaled by 2^-53, so every double is exactly representable. */
    double nextDouble() {
        return (nextLong() >>> 11) * 0x1.0p-53;
    }

    /** Fisher-Yates, in place: for i from n-1 down to 1, swap a[i] with a[nextInt(i+1)]. */
    <T> void shuffle(List<T> list) {
        for (int i = list.size() - 1; i > 0; i--) {
            int j = nextInt(i + 1);
            T tmp = list.get(i);
            list.set(i, list.get(j));
            list.set(j, tmp);
        }
    }
}

/** A point in time to compare the clock against. Shows why a fake clock exists: no sleep in the test. */
record Deadline(Clock clock, long atMillis) {
    boolean expired() {
        return clock.nowMillis() >= atMillis;
    }

    long remainingMillis() {
        return Math.max(0, atMillis - clock.nowMillis());
    }
}

/** Retry timing used by every lesson that talks to a flaky dependency. */
final class Backoff {
    /**
     * Full-jitter exponential backoff: a uniform delay in [0, min(cap, base * 2^attempt)).
     * Deterministic given the rng, which is what makes the retry scheduler in Phase 2 testable.
     */
    static long backoffMillis(int attempt, long baseMillis, long capMillis, SeededRandom rng) {
        if (attempt < 0 || baseMillis <= 0 || capMillis < baseMillis) {
            throw new IllegalArgumentException("attempt >= 0, base > 0, cap >= base required");
        }
        int shift = Math.min(attempt, 30);                 // 2^30 * base already exceeds any sane cap
        long window = Math.min(capMillis, baseMillis << shift);
        return rng.nextInt((int) Math.min(window, Integer.MAX_VALUE));
    }
}

public final class Main {
    public static void main(String[] args) {
        System.out.println("== SeededRandom(42): the same five numbers in Java, Python, and Go ==");
        SeededRandom rng = new SeededRandom(42);
        for (int i = 0; i < 5; i++) System.out.println(rng.nextLong());

        System.out.println("\n== nextInt(100) x 8 and a shuffle of 0..9 with seed 7 ==");
        rng = new SeededRandom(42);
        StringBuilder sb = new StringBuilder();
        for (int i = 0; i < 8; i++) sb.append(rng.nextInt(100)).append(' ');
        System.out.println(sb.toString().strip());
        List<Integer> deck = new ArrayList<>();
        for (int i = 0; i < 10; i++) deck.add(i);
        new SeededRandom(7).shuffle(deck);
        System.out.println(deck);

        System.out.println("\n== FakeClock and a deadline: no Thread.sleep anywhere ==");
        FakeClock clock = new FakeClock(1_700_000_000_000L);        // 2023-11-14T22:13:20Z
        Deadline deadline = new Deadline(clock, clock.nowMillis() + 30_000);
        System.out.println("t+0s     expired=" + deadline.expired() + " remaining=" + deadline.remainingMillis() + "ms");
        clock.advance(29_999);
        System.out.println("t+29.999 expired=" + deadline.expired() + " remaining=" + deadline.remainingMillis() + "ms");
        clock.advance(1);
        System.out.println("t+30s    expired=" + deadline.expired() + " remaining=" + deadline.remainingMillis() + "ms");

        System.out.println("\n== Full-jitter backoff for a PSP call, base 100ms cap 2000ms, seed 42 ==");
        rng = new SeededRandom(42);
        for (int attempt = 0; attempt < 5; attempt++) {
            long window = Math.min(2000, 100L << attempt);
            System.out.printf("attempt %d: window [0, %4d) -> wait %4d ms%n", attempt, window,
                    Backoff.backoffMillis(attempt, 100, 2000, rng));
        }
        try {
            clock.advance(-1);
        } catch (IllegalArgumentException e) {
            System.out.println("\nrejected: " + e.getMessage());
        }
    }
}
