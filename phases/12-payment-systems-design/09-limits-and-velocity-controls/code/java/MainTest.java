// Tests for phases/12-payment-systems-design/09-limits-and-velocity-controls. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.atomic.AtomicInteger;

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

    static final long SECOND = 1000L, MINUTE = 60 * SECOND, HOUR = 60 * MINUTE, DAY = 24 * HOUR;
    static final String ACME = "merchant_acme";

    static Attempt usd(String id, String merchant, String card, String amount) {
        return new Attempt(id, merchant, card, Money.parse(amount, Currency.USD));
    }

    public static void main(String[] args) throws Exception {
        // --- SlidingWindow: the half-open interval (now - W, now] ---
        {
            SlidingWindow w = new SlidingWindow(HOUR);
            w.add(0, 100);
            w.add(10 * SECOND, 200);
            check("window counts both entries inside the hour", w.count(30 * MINUTE) == 2);
            check("window sums amounts inside the hour", w.sumMinor(30 * MINUTE) == 300);
            check("entry exactly W old has left the window", w.count(HOUR) == 1);
            check("sum tracks eviction", w.sumMinor(HOUR) == 200);
            check("next expiry is oldest + W", w.nextExpiryMillis(HOUR) == HOUR + 10 * SECOND);
            check("window empties after everything ages out", w.count(2 * HOUR) == 0 && w.nextExpiryMillis(2 * HOUR) == null);
            checkThrows("window rejects a clock that goes backwards", IllegalArgumentException.class, () -> {
                SlidingWindow back = new SlidingWindow(HOUR);
                back.add(10, 1);
                back.add(5, 1);
            });
        }

        // --- COUNT limit: 5 per card per hour, hard ---
        {
            FakeClock clock = new FakeClock(0);
            Limit perCard = Limit.count("card-attempts-1h", LimitScope.CARD, HOUR, 5, Severity.HARD);
            LimitEngine engine = new LimitEngine(new LimitPolicy().addDefault(perCard), clock);
            for (int i = 1; i <= 5; i++) {
                check("attempt " + i + " of 5 allowed", engine.check(usd("a" + i, ACME, "fp_1", "1.00")).outcome() == Outcome.ALLOW);
                clock.advance(MINUTE);
            }
            Decision sixth = engine.check(usd("a6", ACME, "fp_1", "1.00"));
            check("sixth attempt denied", sixth.outcome() == Outcome.DENY);
            check("denied attempt is not recorded", engine.windowCount(perCard, usd("x", ACME, "fp_1", "1.00")) == 5);
            check("denial names the limit", sixth.reasons().size() == 1 && sixth.reasons().get(0).limitId().equals("card-attempts-1h"));
            check("denial explains observed -> wouldBe vs threshold",
                    sixth.explanations().get(0).contains("5 attempts -> 6 attempts exceeds 5 attempts in 3600s"));
            check("retry-after is until the oldest attempt leaves the window (55 min)", sixth.retryAfterSeconds() == 55 * 60);
            check("a different card is a different window", engine.check(usd("b1", ACME, "fp_2", "1.00")).outcome() == Outcome.ALLOW);
            clock.advance(55 * MINUTE);                                  // now = 1h after attempt 1
            check("one slot frees exactly when the oldest entry is W old", engine.check(usd("a7", ACME, "fp_1", "1.00")).outcome() == Outcome.ALLOW);
            check("and only one slot", engine.check(usd("a8", ACME, "fp_1", "1.00")).outcome() == Outcome.DENY);
        }

        // --- AMOUNT limit, soft: allowed but flagged ---
        {
            FakeClock clock = new FakeClock(0);
            Limit soft = Limit.amount("card-amount-24h", LimitScope.CARD, DAY, Money.parse("500.00", Currency.USD), Severity.SOFT);
            LimitEngine engine = new LimitEngine(new LimitPolicy().addDefault(soft), clock);
            check("480 of 500 allowed", engine.check(usd("s1", ACME, "fp_1", "480.00")).outcome() == Outcome.ALLOW);
            check("exactly reaching the limit is allowed (<=)", engine.check(usd("s2", ACME, "fp_1", "20.00")).outcome() == Outcome.ALLOW);
            Decision over = engine.check(usd("s3", ACME, "fp_1", "0.01"));
            check("soft breach is REVIEW, not DENY", over.outcome() == Outcome.REVIEW);
            check("soft breach still allowed()", over.allowed());
            check("soft breach is recorded (it was allowed)", engine.windowCount(soft, usd("x", ACME, "fp_1", "1.00")) == 3);
            check("soft breach explanation uses Money strings", over.explanations().get(0).contains("500.00 USD -> 500.01 USD exceeds 500.00 USD"));
            check("soft breach contributes no Retry-After", over.retryAfterSeconds() == 0);
            check("EUR attempt is not judged by a USD limit",
                    engine.check(new Attempt("e1", ACME, "fp_1", Money.parse("9000.00", Currency.EUR))).outcome() == Outcome.ALLOW);
        }

        // --- EXPOSURE: open authorizations, released on capture/void ---
        {
            FakeClock clock = new FakeClock(0);
            Limit exposure = Limit.exposure("merchant-exposure", LimitScope.MERCHANT, Money.parse("2000.00", Currency.USD), Severity.HARD);
            LimitEngine engine = new LimitEngine(new LimitPolicy().addDefault(exposure), clock);
            check("first 1500 auth allowed", engine.check(usd("x1", ACME, "fp_1", "1500.00")).outcome() == Outcome.ALLOW);
            check("second 600 auth denied: 2100 open", engine.check(usd("x2", ACME, "fp_2", "600.00")).outcome() == Outcome.DENY);
            clock.advance(30 * DAY);
            check("exposure does not expire with time", engine.check(usd("x3", ACME, "fp_2", "600.00")).outcome() == Outcome.DENY);
            engine.release("x1");
            check("release lowers exposure to zero", engine.openExposure(exposure, usd("q", ACME, "fp_1", "1.00")).minorUnits() == 0);
            check("after capture the 600 auth is allowed", engine.check(usd("x4", ACME, "fp_2", "600.00")).outcome() == Outcome.ALLOW);
            engine.release("x1");
            engine.release("never-seen");
            check("release is idempotent and tolerates unknown ids", engine.openExposure(exposure, usd("q", ACME, "fp_1", "1.00")).minorUnits() == 60000);
            check("other merchant has its own exposure", engine.check(usd("y1", "merchant_other", "fp_9", "1999.99")).outcome() == Outcome.ALLOW);
        }

        // --- Hierarchy: overrides replace defaults by id, never add new ones ---
        {
            LimitPolicy policy = new LimitPolicy()
                    .addDefault(Limit.count("card-attempts-1h", LimitScope.CARD, HOUR, 5, Severity.HARD))
                    .addDefault(Limit.amount("merchant-amount-24h", LimitScope.MERCHANT, DAY, Money.parse("10000.00", Currency.USD), Severity.HARD))
                    .override("merchant_bigbox", Limit.amount("merchant-amount-24h", LimitScope.MERCHANT, DAY, Money.parse("250000.00", Currency.USD), Severity.HARD));
            List<Limit> acme = policy.limitsFor(ACME), bigbox = policy.limitsFor("merchant_bigbox");
            check("acme sees defaults", acme.get(1).maxAmount().equals(Money.parse("10000.00", Currency.USD)));
            check("bigbox sees its override", bigbox.get(1).maxAmount().equals(Money.parse("250000.00", Currency.USD)));
            check("override keeps the card limit untouched", bigbox.get(0).equals(acme.get(0)));
            check("order is stable: defaults' declaration order", acme.size() == 2 && acme.get(0).id().equals("card-attempts-1h"));
            checkThrows("cannot override a limit that has no default", IllegalArgumentException.class,
                    () -> policy.override(ACME, Limit.count("made-up", LimitScope.CARD, HOUR, 1, Severity.HARD)));
            checkThrows("duplicate default id rejected", IllegalArgumentException.class,
                    () -> policy.addDefault(Limit.count("card-attempts-1h", LimitScope.CARD, HOUR, 1, Severity.HARD)));
            FakeClock clock = new FakeClock(0);
            LimitEngine engine = new LimitEngine(policy, clock);
            check("bigbox passes 15k", engine.check(usd("h1", "merchant_bigbox", "fp_1", "15000.00")).outcome() == Outcome.ALLOW);
            check("acme is denied 15k", engine.check(usd("h2", ACME, "fp_1", "15000.00")).outcome() == Outcome.DENY);
        }

        // --- Every breach is reported, and HARD wins over SOFT ---
        {
            FakeClock clock = new FakeClock(0);
            LimitEngine engine = new LimitEngine(Main.standardPolicy(), clock);
            Decision d = engine.check(usd("m1", ACME, "fp_1", "15000.00"));
            check("outcome is DENY when any HARD limit breaks", d.outcome() == Outcome.DENY);
            check("all three breached limits are listed", d.reasons().size() == 3);
            check("soft and hard reasons both present",
                    d.reasons().stream().anyMatch(r -> r.severity() == Severity.SOFT) && d.reasons().stream().anyMatch(r -> r.severity() == Severity.HARD));
            check("denied attempt recorded nowhere", engine.windowCount(Main.standardPolicy().limitsFor(ACME).get(0), usd("x", ACME, "fp_1", "1.00")) == 0);
        }

        // --- Limit construction guards ---
        checkThrows("COUNT needs a positive max", IllegalArgumentException.class, () -> Limit.count("c", LimitScope.CARD, HOUR, 0, Severity.HARD));
        checkThrows("AMOUNT needs a positive money threshold", IllegalArgumentException.class,
                () -> Limit.amount("a", LimitScope.CARD, HOUR, Money.zero(Currency.USD), Severity.HARD));
        checkThrows("attempt amount must be positive", IllegalArgumentException.class, () -> usd("z", ACME, "fp", "0.00"));
        checkThrows("Money rejects currency mix", CurrencyMismatchException.class,
                () -> Money.parse("1.00", Currency.USD).plus(Money.parse("1.00", Currency.EUR)));

        // --- Concurrency: check-and-record is atomic, so a 5-per-hour limit admits exactly 5 of 32 racers ---
        {
            FakeClock clock = new FakeClock(0);
            LimitEngine engine = new LimitEngine(new LimitPolicy()
                    .addDefault(Limit.count("card-attempts-1h", LimitScope.CARD, HOUR, 5, Severity.HARD)), clock);
            int threads = 32;
            CountDownLatch start = new CountDownLatch(1), done = new CountDownLatch(threads);
            AtomicInteger allowed = new AtomicInteger();
            for (int i = 0; i < threads; i++) {
                final int n = i;
                new Thread(() -> {
                    try {
                        start.await();
                        if (engine.check(usd("r" + n, ACME, "fp_race", "1.00")).allowed()) allowed.incrementAndGet();
                    } catch (InterruptedException ignored) {
                    } finally {
                        done.countDown();
                    }
                }).start();
            }
            start.countDown();
            done.await();
            check("exactly 5 of 32 concurrent attempts are admitted", allowed.get() == 5);
        }

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
