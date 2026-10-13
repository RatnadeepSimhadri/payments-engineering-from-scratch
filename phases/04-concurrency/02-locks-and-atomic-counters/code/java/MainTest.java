// Tests for phases/04-concurrency/02-locks-and-atomic-counters. Run: javac -d .out *.java && java -cp .out MainTest
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

    public static void main(String[] args) throws Exception {
        Currency USD = Currency.USD;
        FakeStopwatch clock = new FakeStopwatch(0L, 2_000_000L);

        // The unsafe counter is tested only through an injected interleaving, never by racing threads.
        RacyCounter[] racy = new RacyCounter[1];
        boolean[] fired = {false};
        racy[0] = new RacyCounter(() -> {
            if (!fired[0]) { fired[0] = true; racy[0].add(5); }
        });
        racy[0].add(5);
        check("scripted interleaving loses one update (5 not 10)", racy[0].get() == 5);
        RacyCounter sequential = new RacyCounter();
        for (int i = 0; i < 1_000; i++) sequential.add(1);
        check("racy counter is exact when only one thread uses it", sequential.get() == 1_000);

        // Fix 1: lock. Fix 2: CAS. Both exact under contention.
        BenchmarkResult locked = Benchmark.measure(new LockedCounter(), 4, 5_000, 1, clock);
        check("locked counter exact under 4 threads", locked.exact() && locked.observed() == 20_000);
        AtomicCounter atomic = new AtomicCounter();
        BenchmarkResult cas = Benchmark.measure(atomic, 4, 5_000, 1, clock);
        check("atomic counter exact under 4 threads", cas.exact() && cas.observed() == 20_000);
        AtomicCounter solo = new AtomicCounter();
        for (int i = 0; i < 100; i++) solo.add(3);
        check("CAS never retries without contention", solo.casRetries() == 0 && solo.get() == 300);
        BenchmarkResult negative = Benchmark.measure(new LockedCounter(), 3, 1_000, -7, clock);
        check("negative deltas are exact too", negative.observed() == -21_000 && negative.exact());

        // Fix 3: striped per-account map.
        String[] ids = {"acct-1", "acct-2", "acct-3", "acct-4", "acct-5", "acct-6", "acct-7", "acct-8"};
        StripedAccountMap map = new StripedAccountMap(USD, 4);
        for (String id : ids) map.open(id);
        BenchmarkResult striped = Benchmark.measureMap(map, ids, 8, 2_000, 1, clock);
        check("striped map total exact under 8 threads", striped.exact() && striped.observed() == 16_000);
        boolean perAccount = true;
        for (String id : ids) perAccount &= map.balance(id).minorUnits() == 2_000;
        check("each account received exactly its own 2,000 credits", perAccount);
        check("stripe index is stable and in range",
                map.stripeIndex("acct-1") == map.stripeIndex("acct-1") && map.stripeIndex("acct-1") >= 0 && map.stripeIndex("acct-1") < 4);
        check("stableHash matches the reference value", StripedAccountMap.stableHash("acct-1") == -1423448841);
        checkThrows("unknown account is rejected", IllegalArgumentException.class, () -> map.credit("ghost", Money.ofMinor(1, USD)));
        checkThrows("currency mismatch on credit", CurrencyMismatchException.class, () -> map.credit("acct-1", Money.ofMinor(1, Currency.EUR)));
        checkThrows("negative amount is rejected", IllegalArgumentException.class, () -> map.credit("acct-1", Money.ofMinor(-1, USD)));
        checkThrows("zero stripes is rejected", IllegalArgumentException.class, () -> new StripedAccountMap(USD, 0));

        // Check-then-act: debit cannot overdraw, even when two threads race for the same funds.
        StripedAccountMap wallet = new StripedAccountMap(USD, 2);
        wallet.open("w");
        wallet.credit("w", Money.ofMinor(150, USD));
        checkThrows("debit beyond balance is refused", InsufficientFundsException.class, () -> wallet.debit("w", Money.ofMinor(151, USD)));
        check("refused debit left the balance untouched", wallet.balance("w").minorUnits() == 150);
        CountDownLatch go = new CountDownLatch(1);
        CountDownLatch finished = new CountDownLatch(2);
        AtomicInteger successes = new AtomicInteger();
        for (int t = 0; t < 2; t++) {
            new Thread(() -> {
                try {
                    go.await();
                    wallet.debit("w", Money.ofMinor(100, USD));
                    successes.incrementAndGet();
                } catch (InsufficientFundsException expected) {
                    // exactly one thread lands here
                } catch (InterruptedException e) {
                    Thread.currentThread().interrupt();
                } finally {
                    finished.countDown();
                }
            }).start();
        }
        go.countDown();
        finished.await();
        check("two racing debits of 1.00 from 1.50: exactly one succeeds", successes.get() == 1);
        check("and the balance is 0.50, never negative", wallet.balance("w").minorUnits() == 50);
        check("total sums every account under all stripe locks", wallet.total().equals(Money.ofMinor(50, USD))
                && map.total().minorUnits() == 16_000);

        // Account facade and Money.
        Account acct = new Account("a", USD, new LockedCounter());
        acct.credit(Money.ofMinor(10_000, USD));
        acct.debit(Money.ofMinor(1_999, USD));
        check("account balance 100.00 - 19.99 = 80.01", acct.balance().toString().equals("80.01 USD"));
        checkThrows("account rejects EUR", CurrencyMismatchException.class, () -> acct.credit(Money.ofMinor(1, Currency.EUR)));
        checkThrows("account rejects negative credit", IllegalArgumentException.class, () -> acct.credit(Money.ofMinor(-1, USD)));
        check("Money.plus is exact", Money.ofMinor(110, USD).plus(Money.ofMinor(220, USD)).minorUnits() == 330);

        // Benchmark arithmetic is deterministic with a fake stopwatch.
        BenchmarkResult result = new BenchmarkResult("x", 4, 1_000, 1, 4_000, 2_000_000L);
        check("ops/s = 4,000 ops / 2 ms = 2,000,000", result.opsPerSecond() == 2_000_000.0);
        check("expected and lost", result.expected() == 4_000 && result.lost() == 0 && result.exact());
        check("a short result reports the loss", new BenchmarkResult("x", 2, 10, 1, 17, 1).lost() == 3);
        checkThrows("benchmark rejects zero threads", IllegalArgumentException.class, () -> {
            try { Benchmark.measure(new LockedCounter(), 0, 1, 1, clock); } catch (InterruptedException e) { throw new RuntimeException(e); }
        });

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
