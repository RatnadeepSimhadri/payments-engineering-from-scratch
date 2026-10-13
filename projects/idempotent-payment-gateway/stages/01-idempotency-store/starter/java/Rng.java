// Project: projects/idempotent-payment-gateway — seeded randomness.
// xorshift64* (Vigna, 2016). The same algorithm and seed produce the same sequence in Java, Python, and Go,
// so a retry schedule computed in one language is byte-for-byte the schedule in the others.
// Never use this for anything security-sensitive; it exists so backoff jitter is replayable in tests.

final class Rng {
    private long state;

    Rng(long seed) {
        this.state = seed == 0 ? 0x9E3779B97F4A7C15L : seed;
    }

    /** Next 64-bit output. Callers that need an unsigned value treat the bits as unsigned. */
    long nextLong() {
        long x = state;
        x ^= x >>> 12;
        x ^= x << 25;
        x ^= x >>> 27;
        state = x;
        return x * 0x2545F4914F6CDD1DL;
    }

    /** Uniform integer in [0, bound), using the unsigned interpretation of nextLong(). */
    long nextBelow(long bound) {
        if (bound <= 0) throw new IllegalArgumentException("bound must be positive: " + bound);
        return Long.remainderUnsigned(nextLong(), bound);
    }
}
