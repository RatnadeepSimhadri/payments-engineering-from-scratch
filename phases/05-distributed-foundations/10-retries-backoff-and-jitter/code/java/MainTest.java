// Tests for phases/05-distributed-foundations/10-retries-backoff-and-jitter. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;

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
        // -- the seeded generator is the contract that makes the three languages agree
        Rng rng = new Rng(42);
        check("rng seed 42 first value", rng.nextLong() == 6255019084209693600L);
        check("rng seed 42 second value", rng.nextLong() == -4016670646968046118L);
        check("rng nextBelow stays in range", new Rng(9).nextBelow(7) >= 0 && new Rng(9).nextBelow(7) < 7);
        check("rng is replayable", new Rng(123).nextLong() == new Rng(123).nextLong());
        checkThrows("rng rejects non-positive bound", IllegalArgumentException.class, () -> new Rng(1).nextBelow(0));

        // -- exponential backoff with a cap
        RetryPolicy plain = new RetryPolicy(100, 10_000, 6, Jitter.NONE);
        check("exponential doubles from base", plain.exponential(0) == 100 && plain.exponential(1) == 200 && plain.exponential(4) == 1600);
        check("exponential never exceeds the cap", new RetryPolicy(100, 1_000, 8, Jitter.NONE).exponential(7) == 1000);
        check("exponential does not overflow at absurd attempt counts", new RetryPolicy(100, 10_000, 100, Jitter.NONE).exponential(99) == 10_000);
        check("NONE schedule is exactly the doubling sequence", plain.schedule(new Rng(42)).equals(List.of(100L, 200L, 400L, 800L, 1600L)));
        check("schedule has maxAttempts - 1 entries", plain.schedule(new Rng(1)).size() == 5);
        checkThrows("policy rejects cap below base", IllegalArgumentException.class, () -> new RetryPolicy(100, 50, 3, Jitter.NONE));
        checkThrows("policy rejects zero attempts", IllegalArgumentException.class, () -> new RetryPolicy(100, 100, 0, Jitter.NONE));

        // -- jitter schedules for seed 42 (identical in Python and Go)
        check("FULL jitter schedule, seed 42", new RetryPolicy(100, 10_000, 6, Jitter.FULL).schedule(new Rng(42)).equals(List.of(46L, 26L, 131L, 226L, 241L)));
        check("EQUAL jitter schedule, seed 42", new RetryPolicy(100, 10_000, 6, Jitter.EQUAL).schedule(new Rng(42)).equals(List.of(68L, 179L, 379L, 654L, 994L)));
        check("DECORRELATED jitter schedule, seed 42", new RetryPolicy(100, 10_000, 6, Jitter.DECORRELATED).schedule(new Rng(42)).equals(List.of(190L, 474L, 675L, 569L, 810L)));

        // -- jitter bounds hold for every seed we try
        boolean fullInRange = true, equalInRange = true, decorrInRange = true;
        for (long seed = 1; seed <= 200; seed++) {
            Rng r = new Rng(seed);
            for (int attempt = 0; attempt < 6; attempt++) {
                long exp = plain.exponential(attempt);
                long f = new RetryPolicy(100, 10_000, 6, Jitter.FULL).delay(attempt, 100, r);
                long e = new RetryPolicy(100, 10_000, 6, Jitter.EQUAL).delay(attempt, 100, r);
                long d = new RetryPolicy(100, 10_000, 6, Jitter.DECORRELATED).delay(attempt, 300, r);
                fullInRange &= f >= 0 && f <= exp;
                equalInRange &= e >= exp / 2 && e <= exp;
                decorrInRange &= d >= 100 && d <= 900;
            }
        }
        check("FULL jitter is within [0, exp]", fullInRange);
        check("EQUAL jitter is within [exp/2, exp]", equalInRange);
        check("DECORRELATED jitter is within [base, 3 * previous]", decorrInRange);
        check("DECORRELATED jitter respects the cap", new RetryPolicy(100, 500, 6, Jitter.DECORRELATED).delay(3, 400, new Rng(5)) <= 500);

        // -- the retrier against a scripted operation and a fake clock
        FakeClock clock = new FakeClock(0);
        Retrier retrier = new Retrier(plain, null, clock, new Rng(7));
        Outcome[] flap = {Outcome.TIMEOUT, Outcome.TIMEOUT, Outcome.OK};
        RetryResult r1 = retrier.execute(a -> flap[Math.min(a, flap.length - 1)]);
        check("succeeds on the third attempt", r1.succeeded() && r1.attempts() == 3);
        check("waited exactly 100 + 200 ms on the fake clock", r1.totalDelayMs() == 300 && clock.nowMs() == 300);
        check("delays recorded", r1.delays().equals(List.of(100L, 200L)));

        RetryResult r2 = new Retrier(plain, null, new FakeClock(0), new Rng(7)).execute(a -> Outcome.CARD_DECLINED);
        check("declined card is not retried", r2.stopReason() == StopReason.NON_RETRYABLE && r2.attempts() == 1 && r2.totalDelayMs() == 0);
        RetryResult r3 = new Retrier(plain, null, new FakeClock(0), new Rng(7)).execute(a -> Outcome.BAD_REQUEST);
        check("400 is not retried", r3.stopReason() == StopReason.NON_RETRYABLE);
        RetryResult r4 = new Retrier(new RetryPolicy(100, 10_000, 4, Jitter.NONE), null, new FakeClock(0), new Rng(7)).execute(a -> Outcome.TIMEOUT);
        check("exhausts attempts after 4 tries", r4.stopReason() == StopReason.ATTEMPTS_EXHAUSTED && r4.attempts() == 4);
        check("no sleep after the final failed attempt", r4.totalDelayMs() == 100 + 200 + 400);
        check("retryable classification", Outcome.TIMEOUT.retryable() && Outcome.RATE_LIMITED.retryable() && !Outcome.UNAUTHORIZED.retryable() && !Outcome.OK.retryable());

        // -- retry budget
        RetryBudget budget = new RetryBudget(20, 10);
        check("budget starts empty", budget.tokens() == 0 && !budget.tryWithdraw());
        for (int i = 0; i < 5; i++) budget.deposit();
        check("five deposits at 20% make one token", budget.tokens() == 1 && budget.tryWithdraw() && !budget.tryWithdraw());
        for (int i = 0; i < 1000; i++) budget.deposit();
        check("budget caps at maxTokens", budget.tokens() == 10);
        Retrier budgeted = new Retrier(new RetryPolicy(100, 1_000, 3, Jitter.NONE), budget, new FakeClock(0), new Rng(1));
        int retriesSpent = 0;
        StopReason last = null;
        for (int i = 0; i < 8; i++) {
            RetryResult rr = budgeted.execute(a -> Outcome.SERVER_ERROR);
            retriesSpent += rr.attempts() - 1;
            last = rr.stopReason();
        }
        check("budget stops retries once drained", last == StopReason.BUDGET_EXHAUSTED && budget.tokens() == 0);
        check("retries never exceed banked tokens plus deposits", retriesSpent <= 10 + 8);
        checkThrows("budget rejects percent above 100", IllegalArgumentException.class, () -> new RetryBudget(101, 1));

        // -- the storm: same seed, same answer; jitter lowers the peak the recovered processor sees
        RetryPolicy none = new RetryPolicy(100, 5_000, 8, Jitter.NONE);
        RetryPolicy full = new RetryPolicy(100, 5_000, 8, Jitter.FULL);
        StormReport sNone = RetryStorm.run(200, 40, 100, 1_000, 60, none, 2024);
        StormReport sFull = RetryStorm.run(200, 40, 100, 1_000, 60, full, 2024);
        check("no jitter: every client retries in lockstep", sNone.peakLoad() == 200 && sNone.peakLoadFrom(1_000) == 200);
        check("no jitter: only capacity succeeds per synchronized wave", sNone.succeeded() == 80 && sNone.busyBuckets() == 6);
        check("full jitter: peak after recovery is lower", sFull.peakLoadFrom(1_000) < sNone.peakLoadFrom(1_000));
        check("full jitter: load spread over more buckets", sFull.busyBuckets() > sNone.busyBuckets());
        check("full jitter: everyone eventually succeeds", sFull.succeeded() == 200 && sFull.stillWaiting() == 0);
        check("storm is deterministic for a seed", RetryStorm.run(200, 40, 100, 1_000, 60, full, 2024).peakLoadFrom(1_000) == sFull.peakLoadFrom(1_000)
                && RetryStorm.run(200, 40, 100, 1_000, 60, full, 2024).totalAttempts() == sFull.totalAttempts());
        check("storm accounting: succeeded + gave up + waiting = clients", sNone.succeeded() + sNone.gaveUp() + sNone.stillWaiting() == 200);
        long sum = 0;
        for (long l : sFull.loadPerBucket()) sum += l;
        check("storm accounting: bucket loads sum to total attempts", sum == sFull.totalAttempts());
        StormReport sDecorr = RetryStorm.run(200, 40, 100, 1_000, 60, new RetryPolicy(100, 5_000, 8, Jitter.DECORRELATED), 2024);
        check("decorrelated jitter also beats lockstep", sDecorr.peakLoadFrom(1_000) < sNone.peakLoadFrom(1_000) && sDecorr.succeeded() == 200);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
