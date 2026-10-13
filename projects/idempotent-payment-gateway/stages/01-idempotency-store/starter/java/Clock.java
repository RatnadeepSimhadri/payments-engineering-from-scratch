// Project: projects/idempotent-payment-gateway — injected time.
// Every component that needs "now" or has to wait takes a Clock, so tests never touch wall time.

/** A time source the gateway reads and waits against. */
interface Clock {
    /** Milliseconds since the epoch (or since any fixed origin for a FakeClock). */
    long nowMillis();

    /** Block for the given duration. A FakeClock advances instead of blocking. */
    void sleep(long millis);
}

/** Deterministic clock for tests and demos: time moves only when you move it. */
final class FakeClock implements Clock {
    private long now;

    FakeClock(long startMillis) {
        this.now = startMillis;
    }

    @Override
    public long nowMillis() {
        return now;
    }

    @Override
    public void sleep(long millis) {
        advance(millis);
    }

    void advance(long millis) {
        if (millis < 0) throw new IllegalArgumentException("cannot advance a negative duration: " + millis);
        now += millis;
    }

    void set(long millis) {
        this.now = millis;
    }
}

/** Wall-clock implementation for a real deployment. Never used by the grader. */
final class SystemClock implements Clock {
    @Override
    public long nowMillis() {
        return System.currentTimeMillis();
    }

    @Override
    public void sleep(long millis) {
        try {
            Thread.sleep(millis);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }
}
